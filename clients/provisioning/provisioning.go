// Package provisioningsvc is the Hanzo Cloud provisioning control plane. It
// turns "create a database" into a real logical resource inside an
// already-live, shared product backend, per the unified /v1 binary (HIP-0106).
//
// One HTTP surface, seven kinds, two strategies:
//
// DEDICATED-instance — the four on-demand data add-ons. Each org OWNS its
// instance (an operator Datastore CR in tenant-<org>), so its admin credential
// is naturally tenant-scoped; the assembled DSN is injected as <KIND>_URL into
// the app instance's addons Secret, switching it off Base onto the backend:
//
//	kv        -> Hanzo KV        Datastore type=valkey       redis://…:6379
//	sql       -> Hanzo SQL       Datastore type=postgresql   postgres://…:5432
//	docdb     -> Hanzo DocDB     Datastore type=docdb        mongodb://…:27017
//	datastore -> Hanzo Datastore Datastore type=datastore    datastore://…:8123
//
// SHARED-logical — a logical resource inside an already-live shared backend:
//
//	vector    -> Qdrant      vector.hanzo.svc:6333    PUT /collections/{name}
//	search    -> Meilisearch search.hanzo.svc:7700    POST /indexes
//	s3        -> S3/MinIO     s3.hanzo.svc:9000        MakeBucket
//
// Tenancy: every request is scoped to the gateway-minted org (X-Org-Id /
// c.Org()). Empty org is rejected 403 unless the caller is an admin. The
// physical resource on the shared backend is namespaced "o"<hash(org)>_<name>
// with a FIXED-WIDTH org hash, so the org→name boundary is unambiguous and two
// distinct tenants can never fold onto one backend resource. A global
// UNIQUE(physical_name) guard makes any residual fold fail closed with 409.
//
// Secrets: generated per-resource passwords are sealed in Hanzo KMS
// (client-side encrypted) and only a secret_ref is persisted. When KMS is not
// configured the service degrades safely — it returns the password once in the
// create response and stores NOTHING in plaintext. See kms.go.
package provisioning

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// kinds is the closed set of resource kinds this control plane provisions.
// These strings are the Hanzo product names — never the upstream OSS name of
// the backend (so "sql"/"s3", not "postgres"/"minio").
var kinds = []string{"sql", "vector", "datastore", "kv", "search", "s3", "docdb"}

// unavailableKinds are kinds whose backend cannot currently grant a per-tenant-
// SAFE credential. The control plane REFUSES to provision them — an honest 503
// "not yet available" — rather than mint a cross-tenant capability. This is the
// security bar: never a shared/cluster-wide grant.
//
// It is now EMPTY: datastore + docdb — the only two kinds a shared
// datastore/FerretDB could never scope a per-tenant role on — moved to the
// DEDICATED-instance strategy (dedicated.go, dedicatedEngines), where the org
// owns the whole instance so its admin credential is naturally tenant-scoped.
// The mechanism stays so any FUTURE kind can be honest-gated before it can mint
// a cross-tenant capability; a kind is never both gated and dedicated.
var unavailableKinds = map[string]string{}

// publicEndpoint returns the CUSTOMER-FACING host+port for a provisioned
// resource. The internal admin address (e.g. vector.hanzo.svc:6333) is a
// server-side detail and must NEVER leak to a tenant or into an app config.
// HTTP resources are reachable through the unified api.hanzo.ai gateway
// (path /v1/<kind>/*); native-wire databases have their own public ingress at
// <kind>.hanzo.ai on the native port — so a customer can connect from their app
// (and from hanzo.app) with a real, routable endpoint. Overridable per kind via
// PUBLIC_<KIND>_HOST / PUBLIC_<KIND>_PORT.
func publicEndpoint(kind string) (host string, port int) {
	switch kind {
	case "vector", "search", "docdb", "s3":
		host, port = "api.hanzo.ai", 443 // https://api.hanzo.ai/v1/<kind>/*
	case "sql":
		host, port = "sql.hanzo.ai", 5432
	case "kv":
		host, port = "kv.hanzo.ai", 6379
	case "datastore":
		host, port = "datastore.hanzo.ai", 8123
	default:
		host, port = "api.hanzo.ai", 443
	}
	if h := os.Getenv("PUBLIC_" + strings.ToUpper(kind) + "_HOST"); h != "" {
		host = h
	}
	if p := os.Getenv("PUBLIC_" + strings.ToUpper(kind) + "_PORT"); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	return host, port
}

// endpointFor is the customer-facing host+port for a resource. A DEDICATED
// instance is reached at its OWN in-cluster Service (<instance>.tenant-<org>.svc)
// — the org's app (which also runs in tenant-<org>) dials it directly; there is
// no shared public host for it. A shared-logical resource is reached through the
// public gateway (publicEndpoint). So the two strategies advertise the address
// that actually routes to the resource, never a wrong shared host.
func endpointFor(r Resource) (string, int) {
	if _, dedicated := dedicatedEngines[r.Kind]; dedicated {
		return r.Host, r.Port
	}
	return publicEndpoint(r.Kind)
}

// secretfulKinds are the SHARED-logical kinds whose backend wires a real
// per-resource credential (so the generated password is meaningful and gets
// sealed in KMS / returned once). It is now EMPTY: the only shared-logical kinds
// left are vector, search and s3, which authenticate with a shared, out-of-band
// key, so no per-resource password is produced. The four dedicated add-ons (kv,
// sql, docdb, datastore) DO carry a real per-instance admin credential, but the
// dedicated strategy handles their secret lifecycle directly (see dedicated.go);
// this map only governs the shared-logical path. Kept as the mechanism so a
// FUTURE secretful shared kind can opt in with one entry.
var secretfulKinds = map[string]bool{}

// nameRE constrains the user-supplied resource name to a DNS/identifier-safe
// slug. Validated at the boundary; the physical name and every SQL identifier
// derive from it, so this is the injection guard.
var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// provisionFeeEnvPrefix is the operator knob for the per-provision fee. The
// effective fee is cloud.ResourceFeeCents(provisionFeeEnvPrefix, kind): a
// per-kind override (CLOUD_PROVISION_FEE_CENTS_SQL=…) wins over the global
// CLOUD_PROVISION_FEE_CENTS, else the $1.00 default. Set a kind to 0 to make it
// free (and therefore un-gated).
//
// Ongoing storage footprint (GB-month) is billed by REUSING s.Bill.Meter with a
// usage-derived amount; its unit price lives in hanzoai/pricing
// (infrastructure.blockStorage.pricePerGBMonthly = $0.08/GB-month) and is
// applied by the recurring caller, not at provision time — there is no live-size
// source here and a size is never fabricated.
const provisionFeeEnvPrefix = "CLOUD_PROVISION_FEE_CENTS"

// state is provisioning's own data; shared deps (logger, per-org billing meter,
// brand) live in the embedded cloud.Base, reached as s.Log / s.Bill. The billing
// meter is nil/!Enabled() → Gate allows and Meter is a no-op.
type state struct {
	store *Store
	sec   *secrets
	reg   map[string]Provisioner
	// orch is the cluster orchestrator for the DEDICATED-instance strategy
	// (datastore, docdb). Nil off-cluster: dedicated create then fails closed 503.
	orch orchestrator
	// stopMeter halts the recurring footprint meter (set by startFootprintMeter).
	stopMeter func()
}

type createResp struct {
	ID               string `json:"id"`
	Kind             string `json:"kind"`
	Name             string `json:"name"`
	Status           string `json:"status"`
	Host             string `json:"host"`
	Port             int    `json:"port"`
	Username         string `json:"username,omitempty"`
	Database         string `json:"database"`
	ConnectionString string `json:"connectionString"`
	Password         string `json:"password,omitempty"`
}

type getResp struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Status   string `json:"status"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Database string `json:"database"`
}

type listItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Host      string `json:"host"`
	Port      int    `json:"port"`
	CreatedAt int64  `json:"createdAt"`
}

// Mount wires the provisioning surface onto app per HIP-0106. It is the complex
// flavour of the generic subsystem: it keeps a package global (mounted) for
// cross-package reach and starts a recurring footprint meter, so it constructs the
// Service value directly rather than through cloud.Mount.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("provisioning.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("provisioning.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("provisioning.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("provisioning.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "provisioning.db"))
	if err != nil {
		return fmt.Errorf("provisioning.Mount: open store: %w", err)
	}

	b := cloud.NewBase(deps, "provisioning")
	s := &cloud.Service[state]{Base: b, State: state{
		store: store,
		sec:   openSecrets(deps.Brand, b.Log),
		reg:   newRegistry(),
		orch:  newOrchestrator(),
	}}
	mounted = s

	routes(app, s)

	// Recurring per-org footprint meter for running dedicated instances.
	startFootprintMeter(s)

	b.Log.Info("provisioning mounted",
		"kinds", len(kinds),
		"dedicated", len(dedicatedEngines),
		"cluster", s.State.orch != nil && s.State.orch.Ready() == nil,
		"kms", s.State.sec.Enabled(),
		"brand", deps.Brand,
		"env", deps.Env,
		"billing", s.Bill.Enabled(),
	)
	return nil
}

// routes registers the CRUD surface for each provisionable kind (the same handler
// factories bound per kind).
func routes(app *zip.App, s *cloud.Service[state]) {
	for _, kind := range kinds {
		k := kind
		app.Post("/v1/"+k, create(s, k))
		app.Get("/v1/"+k, list(s, k))
		app.Get("/v1/"+k+"/:name", get(s, k))
		app.Delete("/v1/"+k+"/:name", drop(s, k))
	}
}

// create provisions a new resource of kind for the caller's org. Two strategies
// share one preamble (auth, name validation, billing gate, dedup): the shared-
// logical kinds create a resource inside a live shared backend; the dedicated
// kinds (datastore, docdb) launch the org's OWN instance (createDedicated).
func create(s *cloud.Service[state], kind string) zip.Handler {
	return func(c *zip.Ctx) error {
		org, ok := tenant(c)
		if !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}

		var body struct {
			Name string `json:"name"`
			// Instance binds a DEDICATED add-on to the app instance whose
			// <instance>-addons Secret receives the <KIND>_URL (e.g. "commerce").
			// Optional: empty means "not instance-bound" — the DSN is returned once
			// and wired by the caller (the pre-instance-binding behavior).
			Instance string `json:"instance"`
		}
		if err := c.Bind(&body); err != nil {
			return err
		}
		name := strings.ToLower(strings.TrimSpace(body.Name))
		if !nameRE.MatchString(name) {
			return zip.ErrBadRequest("name must match ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
		}
		// instance keys a k8s Secret name (<instance>-addons); constrain it to the
		// same DNS/identifier-safe slug as name so it can never inject a malformed
		// or path-traversing Secret reference. Empty is allowed (no binding).
		instance := strings.ToLower(strings.TrimSpace(body.Instance))
		if instance != "" && !nameRE.MatchString(instance) {
			return zip.ErrBadRequest("instance must match ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
		}

		// Honest availability gate (now empty, kept as the mechanism). Refuse a
		// gated kind BEFORE billing or any write, so a customer is never handed a
		// cross-tenant capability nor charged for a resource we won't create. A
		// dedicated kind is never in this map.
		if reason, gated := unavailableKinds[kind]; gated {
			return zip.Errorf(http.StatusServiceUnavailable, "%s", reason)
		}

		ctx := c.Context()

		// Pre-provision balance gate (fail-closed, per-org). Refuse BEFORE any
		// resource is created: an unfunded org — or, in the default fail-closed
		// posture, an unreachable commerce — gets 402/503 and nothing is
		// provisioned. Scoped to THIS caller's org (the same slug that namespaces
		// the resource, derived from a validated JWT, not a spoofable header), so
		// the charge can never target another tenant. fee is computed once and
		// reused by the post-success debit; fee==0 or unconfigured billing makes
		// this a no-op. Applies to BOTH strategies.
		fee := cloud.ResourceFeeCents(provisionFeeEnvPrefix, kind)
		project, projectValidated := principal.ValidatedProject(c)
		if err := s.Bill.Gate(ctx, principal.Payer(c), project, projectValidated, kind, fee); err != nil {
			return cloud.DenyResource(c, err)
		}

		// Fast duplicate check (the UNIQUE index is the authoritative guard).
		if _, err := s.State.store.Get(ctx, org, kind, name); err == nil {
			return zip.ErrConflict("resource already exists")
		} else if !errors.Is(err, errNotFound) {
			return zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
		}

		// DEDICATED-instance strategy (datastore, docdb): the org's OWN isolated
		// instance, launched via an operator Datastore CR in tenant-<org>.
		if e, dedicated := dedicatedEngines[kind]; dedicated {
			return createDedicated(s, c, ctx, kind, org, name, e, fee, instance)
		}

		// SHARED-logical strategy (sql, vector, kv, search, s3).
		prov := s.State.reg[kind]
		if prov == nil {
			return zip.Errorf(http.StatusNotImplemented, "kind %q not supported", kind)
		}
		physical := physicalName(org, name)

		// Global uniqueness guard (across ALL orgs/kinds). The fixed-width org
		// hash already makes a cross-tenant fold cryptographically negligible;
		// this check plus the UNIQUE(physical_name) index make any residual fold
		// (or hash collision) FAIL CLOSED with 409 BEFORE the backend is touched
		// — never a silent shared resource, which on KV would be a cross-tenant
		// credential takeover (idempotent ACL SETUSER overwriting another
		// tenant's user) and elsewhere a cross-tenant DoS / existence oracle.
		if exists, err := s.State.store.PhysicalExists(ctx, physical); err != nil {
			return zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
		} else if exists {
			return zip.ErrConflict("resource already exists")
		}

		user := physical
		pw, err := genToken(24)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
		}

		cs, host, port, db, err := prov.Create(ctx, physical, user, pw)
		if err != nil {
			if errors.Is(err, errAlreadyExists) {
				return zip.ErrConflict("resource already exists")
			}
			s.Log.Error("provision failed", "kind", kind, "org", org, "name", name, "err", err)
			return zip.Errorf(http.StatusBadGateway, "provision %s failed: %v", kind, err)
		}

		// Secret handling. Only secretful kinds carry a real per-resource
		// password. Seal it in KMS when configured; otherwise return once and
		// store nothing (never plaintext).
		secretRef := fmt.Sprintf("org/%s/%s/%s", org, kind, name)
		storedRef, returnPw, username := "", "", ""
		if secretfulKinds[kind] {
			returnPw, username = pw, user
			if s.State.sec.Enabled() {
				if err := s.State.sec.Put(secretRef, []byte(pw)); err != nil {
					_ = prov.Drop(ctx, physical, user)
					s.Log.Error("kms put failed; rolled back backend", "kind", kind, "err", err)
					return zip.Errorf(http.StatusInternalServerError, "store secret failed")
				}
				storedRef = secretRef
			} else {
				s.Log.Warn("KMS degraded: password returned once, not persisted", "kind", kind, "org", org, "name", name)
			}
		}

		id, err := genID()
		if err != nil {
			_ = prov.Drop(ctx, physical, user)
			if storedRef != "" {
				_ = s.State.sec.Delete(storedRef)
			}
			return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
		}

		r := Resource{
			ID: id, Org: org, Kind: kind, Name: name,
			PhysicalName: physical, SecretRef: storedRef,
			Host: host, Port: port, Username: username, DBName: db,
			Status: "ready", CreatedAt: time.Now().Unix(),
		}
		if err := s.State.store.Insert(ctx, r); err != nil {
			// Lost a concurrent race or DB error — undo the backend + secret.
			_ = prov.Drop(ctx, physical, user)
			if storedRef != "" {
				_ = s.State.sec.Delete(storedRef)
			}
			if errors.Is(err, errConflict) {
				return zip.ErrConflict("resource already exists")
			}
			return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
		}

		// Resource is live + persisted — debit the caller's org ledger for the
		// provision (per-org, env-attributed, async best-effort so the debit never
		// blocks or corrupts this 201; a debit failure is logged for
		// reconciliation). Recurring storage footprint reuses s.Bill.Meter with a
		// GB-month amount once a live-size source exists.
		s.Bill.Meter(principal.Payer(c), principal.Project(c), kind, fee, c.RequestID(), cloud.ClientIP(c))

		// Return the PUBLIC endpoint, never the internal admin host. Remap the
		// connection string's host:port too so a copy-pasted DSN is routable.
		ph, pp := publicEndpoint(kind)
		pubCS := cs
		if cs != "" {
			pubCS = strings.ReplaceAll(cs, fmt.Sprintf("%s:%d", host, port), fmt.Sprintf("%s:%d", ph, pp))
		}
		return c.JSON(http.StatusCreated, createResp{
			ID: id, Kind: kind, Name: name, Status: "ready",
			Host: ph, Port: pp, Username: username, Database: db,
			ConnectionString: pubCS, Password: returnPw,
		})
	}
}

// list returns every resource of kind for the caller's org. Never a password.
func list(s *cloud.Service[state], kind string) zip.Handler {
	return func(c *zip.Ctx) error {
		org, ok := tenant(c)
		if !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}
		rows, err := s.State.store.List(c.Context(), org, kind)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
		}
		out := make([]listItem, 0, len(rows))
		for _, r := range rows {
			host, port := endpointFor(r)
			out = append(out, listItem{
				ID: r.ID, Name: r.Name, Kind: r.Kind, Status: r.Status,
				Host: host, Port: port, CreatedAt: r.CreatedAt,
			})
		}
		return c.JSON(http.StatusOK, out)
	}
}

// get returns one resource's metadata. Never a password.
func get(s *cloud.Service[state], kind string) zip.Handler {
	return func(c *zip.Ctx) error {
		org, ok := tenant(c)
		if !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}
		name := strings.ToLower(strings.TrimSpace(c.Param("name")))
		ctx := c.Context()
		r, err := s.State.store.Get(ctx, org, kind, name)
		if errors.Is(err, errNotFound) {
			return zip.ErrNotFound("resource not found")
		}
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
		}
		// For a dedicated instance, reconcile provisioning -> ready from the
		// operator's live CR status before answering (honest readiness).
		if _, dedicated := dedicatedEngines[r.Kind]; dedicated {
			r = reconcileDedicated(s, ctx, r)
		}
		host, port := endpointFor(r)
		return c.JSON(http.StatusOK, getResp{
			ID: r.ID, Name: r.Name, Kind: r.Kind, Status: r.Status,
			Host: host, Port: port, Username: r.Username, Database: r.DBName,
		})
	}
}

// drop deprovisions the backend resource, deletes the sealed secret, and
// removes the metadata row.
func drop(s *cloud.Service[state], kind string) zip.Handler {
	return func(c *zip.Ctx) error {
		org, ok := tenant(c)
		if !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}
		name := strings.ToLower(strings.TrimSpace(c.Param("name")))
		ctx := c.Context()

		r, err := s.State.store.Get(ctx, org, kind, name)
		if errors.Is(err, errNotFound) {
			return zip.ErrNotFound("resource not found")
		}
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
		}

		if _, dedicated := dedicatedEngines[kind]; dedicated {
			// Revert the app instance to Base FIRST: remove the <KIND>_URL from the
			// addons Secret so the app stops using this backend BEFORE we tear it
			// down (never leave a live instance pointed at a deleted backend). Fail
			// closed — block the teardown on error so a retry finishes the revert;
			// drop is idempotent. No-op when the resource is not instance-bound.
			if err := removeAddonURL(s, ctx, org, r.Instance, kind); err != nil {
				s.Log.Error("revert instance to base failed", "kind", kind, "org", org, "name", name, "instance", r.Instance, "err", err)
				return zip.Errorf(http.StatusBadGateway, "revert instance: %v", err)
			}
			// Tear down the org's dedicated instance (CR + admin Secret); the
			// operator GCs the StatefulSet + Service + PVC. Removing the row below
			// also stops the recurring footprint meter for this instance.
			if err := dropDedicated(s, ctx, r); err != nil {
				s.Log.Error("deprovision instance failed", "kind", kind, "org", org, "name", name, "err", err)
				return zip.Errorf(http.StatusBadGateway, "deprovision %s failed: %v", kind, err)
			}
		} else if prov := s.State.reg[kind]; prov != nil {
			if err := prov.Drop(ctx, r.PhysicalName, r.Username); err != nil {
				s.Log.Error("deprovision failed", "kind", kind, "org", org, "name", name, "err", err)
				return zip.Errorf(http.StatusBadGateway, "deprovision %s failed: %v", kind, err)
			}
		}
		if r.SecretRef != "" {
			if err := s.State.sec.Delete(r.SecretRef); err != nil {
				s.Log.Warn("kms delete failed (continuing)", "ref", r.SecretRef, "err", err)
			}
		}
		if _, err := s.State.store.Delete(ctx, org, kind, name); err != nil {
			return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
		}
		return c.NoContent(http.StatusNoContent)
	}
}

// ----- tenancy + naming -----------------------------------------------------

// tenant resolves the org for a request. Empty org is allowed only for admins,
// who are bucketed under the literal "admin" org.
//
// REQUIRES A VALIDATED PRINCIPAL (RED HIGH). SanitizeIdentity strips X-User-Id on
// ingress and re-sets it ONLY for a validated bearer/cookie; on the no-principal
// "Phase-1 data path" it RESTORES the client's raw X-Org-Id but leaves X-User-Id
// empty. This control plane ALLOCATES and DESTROYS real backend resources and
// returns generated credentials, so trusting X-Org-Id alone let an in-cluster
// caller (a co-namespace pod within the cloud-api NetworkPolicy) forge
// `X-Org-Id: victim` with NO bearer and provision a DB in the victim's namespace
// (receiving its connection string + password), destroy the victim's database, or
// enumerate its resources — strictly worse than a data read. Gating on c.User()
// (X-User-Id) refuses ONLY that anonymous-forge path: every legitimate caller
// arrives through the console/gateway with a validated principal, so no real
// client breaks.
//
// The X-User-IsAdmin claim is likewise only trustworthy under a validated
// principal — SanitizeIdentity sets it only for a JWT-verified SuperAdmin
// (HIP-0026) — and even then reaches only the literal "admin" org's own physical
// namespace, never a real tenant's.
func tenant(c *zip.Ctx) (string, bool) {
	if !principal.Validated(c) {
		return "", false // no validated principal — refuse the forgeable data path
	}
	org := sanitizeOrg(c.Org())
	if org != "" {
		return org, true
	}
	if c.IsAdmin() {
		return "admin", true
	}
	return "", false
}

// sanitizeOrg reduces a gateway org id to a lowercase [a-z0-9-] slug that is
// INJECTIVE in the raw owner. It delegates to cloud.SanitizeOrg — the ONE org-slug
// normalizer for the tenant layer — so the slug this control plane keys its
// bucket/DB names on is byte-identical to the one cloud.OrgDB folds every
// per-tenant SQLite path through (and to what S3/KMS/knowledge derive). The
// isolation-boundary rationale (refuse unsafe-rune owners; identity on a clean
// DNS-1123 label; else fold + "-"+SHA-256(raw)[:8], with the suffixed-shape
// fast-path exclusion) lives with the implementation in cloud/orgdb.go.
func sanitizeOrg(s string) string { return cloud.SanitizeOrg(s) }

// orgHash returns a fixed-width, collision-resistant tag for an org slug: the
// first 16 hex chars (64 bits) of SHA-256(org). The FIXED WIDTH is the whole
// point — it makes the org→name boundary in physicalName / bucketName
// unambiguous, so two distinct orgs can never fold onto one backend resource.
// The prior "org_<org>_<name>" join folded that boundary: physicalName(
// "acme","my-db") == physicalName("acme-my","db") == "org_acme_my_db", a
// cross-tenant collision (credential takeover on KV; DoS/existence oracle
// elsewhere). 64 bits makes a cross-org collision cryptographically negligible.
func orgHash(org string) string {
	sum := sha256.Sum256([]byte(org))
	return hex.EncodeToString(sum[:])[:16]
}

// sanitizeIdent reduces a validated resource name to a [a-z0-9_] identifier by
// folding '-' (the only non-alphanumeric a valid name may contain) to '_'.
// Names are constrained by nameRE at the boundary and never contain '_', so the
// fold round-trips and is injective on the valid set.
func sanitizeIdent(name string) string { return strings.ReplaceAll(name, "-", "_") }

// physicalName namespaces a resource on a shared backend as
// "o"<orgHash>_<sanitizedName>. The leading 'o' keeps it alpha-initial (a valid
// identifier for every backend); the fixed-width org hash disambiguates org
// from name; sanitizeIdent makes the name a safe SQL/datastore/Base identifier.
// Injective in (org,name) up to a 64-bit SHA-256 collision. With
// name ≤ 40 chars (nameRE) the identifier is ≤ 58 chars — inside Postgres's
// 63-char identifier limit.
func physicalName(org, name string) string {
	return "o" + orgHash(org) + "_" + sanitizeIdent(name)
}

// BucketName + BucketPrefix export the tenant→S3-bucket naming so the ONE
// convention is shared, not re-implemented. clients/s3 (the /v1/s3 file manager)
// operates on the SAME buckets this control plane allocates for kind "s3", so a
// bucket provisioned via POST /v1/s3 {name:x} is browsable there as bucket "x".
// The two subsystems MUST derive the S3 bucket name identically or the tenant
// boundary drifts between "allocate" and "operate" — and worse, a file manager
// using the raw physical name would try to create an underscore-containing bucket
// that S3 rejects. The full derivation is bucketName(physicalName(org,name)): the
// fixed-width org-hash prefix makes it injective in (org,name); the '_'→'-' fold
// makes it a DNS-safe S3 name. clients/s3 imports provisioning for exactly these;
// the dependency is one-directional (provisioning never imports s3), so no cycle.
func BucketName(org, name string) string { return bucketName(physicalName(org, name)) }

// BucketPrefix is the S3-bucket-name prefix ALL of an org's buckets share
// (== bucketName("o"+orgHash(org))+"-"). clients/s3 lists all buckets and filters
// to this prefix (the caller only ever sees its own), then strips it to recover
// friendly names. Derived through bucketName so it matches the real bucket names
// exactly, INCLUDING the '_'→'-' fold of the org-hash separator.
func BucketPrefix(org string) string { return bucketName("o"+orgHash(org)) + "-" }

// SanitizeOrg exports the org-slug normalizer so clients/s3 derives the caller's
// org tag from the SAME reduced slug this control plane keys on — otherwise a
// bucket created here (keyed on the sanitized org) would be invisible to a file
// manager that hashed the raw org, and vice-versa.
func SanitizeOrg(org string) string { return sanitizeOrg(org) }

func genID() (string, error) {
	tok, err := genToken(12)
	if err != nil {
		return "", err
	}
	return "rs_" + tok, nil
}

// mounted is the active service, set by Mount so Shutdown can release the
// metadata store. The unified binary mounts one provisioning surface.
var mounted *cloud.Service[state]

// Shutdown closes the provisioning metadata store. Idempotent. Mirrors the
// plansvc Shutdown contract so the serve layer can release subsystem resources
// uniformly.
func Shutdown(context.Context) error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	if mounted.State.stopMeter != nil {
		mounted.State.stopMeter()
		mounted.State.stopMeter = nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}
