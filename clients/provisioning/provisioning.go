// Package provisioningsvc is the Hanzo Cloud provisioning control plane. It
// turns "create a database" into a real logical resource inside an
// already-live, shared product backend, per the unified /v1 binary (HIP-0106).
//
// One HTTP surface, seven kinds, one Provisioner each:
//
//	sql       -> Postgres    sql.hanzo.svc:5432       CREATE DATABASE + ROLE
//	vector    -> Qdrant      vector.hanzo.svc:6333    PUT /collections/{name}
//	datastore -> ClickHouse  datastore.hanzo.svc:8123 CREATE DATABASE + USER
//	kv        -> Redis       kv.hanzo.svc:6379        ACL SETUSER (keyspace scope)
//	search    -> Meilisearch search.hanzo.svc:7700    POST /indexes
//	s3        -> S3/MinIO     s3.hanzo.svc:9000        MakeBucket
//	docdb     -> MongoDB      docdb.hanzo.svc:27017    createCollection + createUser
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
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// kinds is the closed set of resource kinds this control plane provisions.
// These strings are the Hanzo product names — never the upstream OSS name of
// the backend (so "sql"/"s3", not "postgres"/"minio").
var kinds = []string{"sql", "vector", "datastore", "kv", "search", "s3", "docdb"}

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

// secretfulKinds are the kinds whose backend wires a real per-resource
// credential (so the generated password is meaningful and gets sealed in KMS /
// returned once). The others (vector, search, storage) authenticate with a
// shared, out-of-band key, so no per-resource password is produced.
var secretfulKinds = map[string]bool{
	"sql":       true,
	"kv":        true,
	"datastore": true,
	"docdb":     true,
}

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
// Ongoing storage footprint (GB-month) is billed by REUSING s.bill.Meter with a
// usage-derived amount; its unit price lives in hanzoai/pricing
// (infrastructure.blockStorage.pricePerGBMonthly = $0.08/GB-month) and is
// applied by the recurring caller, not at provision time — there is no live-size
// source here and a size is never fabricated.
const provisionFeeEnvPrefix = "CLOUD_PROVISION_FEE_CENTS"

type svc struct {
	store *Store
	sec   *secrets
	reg   map[string]Provisioner
	log   luxlog.Logger
	// bill is the shared per-org resource gate+meter (reuses deps.Metering, the
	// one commerce client). Nil/!Enabled() makes Gate allow and Meter a no-op.
	bill *cloud.ResourceMeter
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

// Mount wires the provisioning surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("provisioning.Mount: nil zip.App")
	}
	log := deps.Logger
	if log == nil {
		return fmt.Errorf("provisioning.Mount: nil deps.Logger")
	}
	log = log.New("subsystem", "provisioning")

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

	s := &svc{
		store: store,
		sec:   openSecrets(deps.Brand, log),
		reg:   newRegistry(),
		log:   log,
		bill:  cloud.NewResourceMeter(deps, "provisioning"),
	}
	mounted = s

	for _, kind := range kinds {
		k := kind
		app.Post("/v1/"+k, s.create(k))
		app.Get("/v1/"+k, s.list(k))
		app.Get("/v1/"+k+"/:name", s.get(k))
		app.Delete("/v1/"+k+"/:name", s.drop(k))
	}

	log.Info("provisioning mounted",
		"kinds", len(kinds),
		"kms", s.sec.Enabled(),
		"brand", deps.Brand,
		"env", deps.Env,
		"billing", s.bill.Enabled(),
	)
	return nil
}

func init() {
	cloud.Register("provisioning", 120, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("provisioning.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
}

// create provisions a new logical resource of kind for the caller's org.
func (s *svc) create(kind string) zip.Handler {
	return func(c *zip.Ctx) error {
		org, ok := tenant(c)
		if !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}
		prov := s.reg[kind]
		if prov == nil {
			return zip.Errorf(http.StatusNotImplemented, "kind %q not supported", kind)
		}

		var body struct {
			Name string `json:"name"`
		}
		if err := c.Bind(&body); err != nil {
			return err
		}
		name := strings.ToLower(strings.TrimSpace(body.Name))
		if !nameRE.MatchString(name) {
			return zip.ErrBadRequest("name must match ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
		}

		ctx := c.Context()

		// Pre-provision balance gate (fail-closed, per-org). Refuse BEFORE any
		// backend resource is created: an unfunded org — or, in the default
		// fail-closed posture, an unreachable commerce — gets 402/503 and nothing
		// is provisioned (no free provisioning). Scoped to THIS caller's org (the
		// same slug that namespaces the resource below and that #66's identity
		// sanitizer derives from a validated JWT, not a spoofable header), so the
		// charge can never target another tenant. fee is computed once and reused
		// by the post-success debit; fee==0 (a free kind) or unconfigured billing
		// makes this a no-op.
		fee := cloud.ResourceFeeCents(provisionFeeEnvPrefix, kind)
		if err := s.bill.Gate(ctx, org, kind, fee); err != nil {
			return cloud.DenyResource(c, err)
		}

		// Fast duplicate check (the UNIQUE index is the authoritative guard).
		if _, err := s.store.Get(ctx, org, kind, name); err == nil {
			return zip.ErrConflict("resource already exists")
		} else if !errors.Is(err, errNotFound) {
			return zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
		}

		physical := physicalName(org, name)

		// Global uniqueness guard (across ALL orgs/kinds). The fixed-width org
		// hash already makes a cross-tenant fold cryptographically negligible;
		// this check plus the UNIQUE(physical_name) index make any residual fold
		// (or hash collision) FAIL CLOSED with 409 BEFORE the backend is touched
		// — never a silent shared resource, which on KV would be a cross-tenant
		// credential takeover (idempotent ACL SETUSER overwriting another
		// tenant's user) and elsewhere a cross-tenant DoS / existence oracle.
		if exists, err := s.store.PhysicalExists(ctx, physical); err != nil {
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
			s.log.Error("provision failed", "kind", kind, "org", org, "name", name, "err", err)
			return zip.Errorf(http.StatusBadGateway, "provision %s failed: %v", kind, err)
		}

		// Secret handling. Only secretful kinds carry a real per-resource
		// password. Seal it in KMS when configured; otherwise return once and
		// store nothing (never plaintext).
		secretRef := fmt.Sprintf("org/%s/%s/%s", org, kind, name)
		storedRef, returnPw, username := "", "", ""
		if secretfulKinds[kind] {
			returnPw, username = pw, user
			if s.sec.Enabled() {
				if err := s.sec.Put(secretRef, []byte(pw)); err != nil {
					_ = prov.Drop(ctx, physical, user)
					s.log.Error("kms put failed; rolled back backend", "kind", kind, "err", err)
					return zip.Errorf(http.StatusInternalServerError, "store secret failed")
				}
				storedRef = secretRef
			} else {
				s.log.Warn("KMS degraded: password returned once, not persisted", "kind", kind, "org", org, "name", name)
			}
		}

		id, err := genID()
		if err != nil {
			_ = prov.Drop(ctx, physical, user)
			if storedRef != "" {
				_ = s.sec.Delete(storedRef)
			}
			return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
		}

		r := Resource{
			ID: id, Org: org, Kind: kind, Name: name,
			PhysicalName: physical, SecretRef: storedRef,
			Host: host, Port: port, Username: username, DBName: db,
			Status: "ready", CreatedAt: time.Now().Unix(),
		}
		if err := s.store.Insert(ctx, r); err != nil {
			// Lost a concurrent race or DB error — undo the backend + secret.
			_ = prov.Drop(ctx, physical, user)
			if storedRef != "" {
				_ = s.sec.Delete(storedRef)
			}
			if errors.Is(err, errConflict) {
				return zip.ErrConflict("resource already exists")
			}
			return zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
		}

		// Resource is live + persisted — debit the caller's org ledger for the
		// provision (per-org, env-attributed, async best-effort so the debit never
		// blocks or corrupts this 201; a debit failure is logged for
		// reconciliation). Recurring storage footprint reuses s.bill.Meter with a
		// GB-month amount once a live-size source exists.
		s.bill.Meter(org, kind, fee, c.RequestID(), cloud.ClientIP(c))

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
func (s *svc) list(kind string) zip.Handler {
	return func(c *zip.Ctx) error {
		org, ok := tenant(c)
		if !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}
		rows, err := s.store.List(c.Context(), org, kind)
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
		}
		out := make([]listItem, 0, len(rows))
		for _, r := range rows {
			ph, pp := publicEndpoint(r.Kind)
			out = append(out, listItem{
				ID: r.ID, Name: r.Name, Kind: r.Kind, Status: r.Status,
				Host: ph, Port: pp, CreatedAt: r.CreatedAt,
			})
		}
		return c.JSON(http.StatusOK, out)
	}
}

// get returns one resource's metadata. Never a password.
func (s *svc) get(kind string) zip.Handler {
	return func(c *zip.Ctx) error {
		org, ok := tenant(c)
		if !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}
		name := strings.ToLower(strings.TrimSpace(c.Param("name")))
		r, err := s.store.Get(c.Context(), org, kind, name)
		if errors.Is(err, errNotFound) {
			return zip.ErrNotFound("resource not found")
		}
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
		}
		ph, pp := publicEndpoint(r.Kind)
		return c.JSON(http.StatusOK, getResp{
			ID: r.ID, Name: r.Name, Kind: r.Kind, Status: r.Status,
			Host: ph, Port: pp, Username: r.Username, Database: r.DBName,
		})
	}
}

// drop deprovisions the backend resource, deletes the sealed secret, and
// removes the metadata row.
func (s *svc) drop(kind string) zip.Handler {
	return func(c *zip.Ctx) error {
		org, ok := tenant(c)
		if !ok {
			return zip.ErrForbidden("X-Org-Id required")
		}
		name := strings.ToLower(strings.TrimSpace(c.Param("name")))
		ctx := c.Context()

		r, err := s.store.Get(ctx, org, kind, name)
		if errors.Is(err, errNotFound) {
			return zip.ErrNotFound("resource not found")
		}
		if err != nil {
			return zip.Errorf(http.StatusInternalServerError, "get: %v", err)
		}

		if prov := s.reg[kind]; prov != nil {
			if err := prov.Drop(ctx, r.PhysicalName, r.Username); err != nil {
				s.log.Error("deprovision failed", "kind", kind, "org", org, "name", name, "err", err)
				return zip.Errorf(http.StatusBadGateway, "deprovision %s failed: %v", kind, err)
			}
		}
		if r.SecretRef != "" {
			if err := s.sec.Delete(r.SecretRef); err != nil {
				s.log.Warn("kms delete failed (continuing)", "ref", r.SecretRef, "err", err)
			}
		}
		if _, err := s.store.Delete(ctx, org, kind, name); err != nil {
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
// principal — SanitizeIdentity sets it only for a JWT-verified global admin
// (HIP-0026) — and even then reaches only the literal "admin" org's own physical
// namespace, never a real tenant's.
func tenant(c *zip.Ctx) (string, bool) {
	if c.User() == "" {
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
// INJECTIVE in the raw owner: an org bearing any whitespace/control/format rune
// is REFUSED (→ "") at the boundary — folding it would not survive TrimSpace /
// transport OWS-trim and would collapse "acme " onto "acme" (RED CRIT-2
// residual) — and every accepted owner then maps to the identity on a DNS-1123
// label, else a folded slug disambiguated with "-" + the first 16 hex of
// SHA-256(raw owner). Without the suffix the fold was lossy —
// ToLower + every non-[a-z0-9-]→"-" + a 32-char truncation collapse distinct
// owners (`Acme`/`acme`, `team.a`/`team-a`) onto one slug, and since the whole
// tenant→bucket/DB namespace hashes THIS slug (orgHash, physicalName), that was a
// cross-tenant collision: two orgs sharing one physical namespace (RED MED, proven
// reachable because the IAM org name is a varchar with no shape validator, so a
// fold-sibling of a target org is registerable and mints a valid token). Uses the
// SAME disambiguation STRATEGY as iam/object/orgdb.go:orgSlug (fold + SHA-256
// suffix) — not byte-identical (this caps the identity branch at 32 chars and
// truncates the fold, since the slug keys cloud's own bucket/DB names, which stay
// consistent across allocate + operate; IAM's orgSlug keys only its per-org
// SQLite files). The org still flows into physical identifiers, so this is the
// isolation boundary.
//
// The identity fast-path is withheld from a clean slug that ITSELF looks like a
// suffixed output (`<label>-<16 lowercase hex>`): such a slug is ambiguous with a
// folded owner's disambiguation, so it too is re-suffixed — otherwise a squatted
// org literally named "foo-<sha256(Foo)[:8]>" would alias non-slug owner "Foo".
// Real org names essentially never end in "-"+16-hex, so this shifts nothing real
// while closing the alias class completely (every non-identity output carries a
// hash of its OWN raw bytes, so two distinct raws collide only on a full 64-bit
// SHA-256 prefix collision).
func sanitizeOrg(s string) string {
	// Reject at the boundary — an empty org, or one carrying any whitespace /
	// control / zero-width-format rune, is a NON-INJECTIVE tenant identifier:
	// strings.TrimSpace (here or upstream) and fasthttp's header-value OWS trim
	// silently collapse "acme " onto "acme", so distinct IAM orgs would fold onto
	// one physical namespace/bucket/DB. Refusing (→ "", which every caller gates
	// on) is fail-secure and, with the raw-byte hash below, makes the map
	// injective end-to-end (RED CRIT-2 residual). This mirrors cloud.OrgHasUnsafeRune
	// at the identity middleware; enforcing it HERE too covers non-header callers
	// (clients/s3 derives its org tag through SanitizeOrg, not the request org).
	if s == "" || cloud.OrgHasUnsafeRune(s) {
		return ""
	}
	lower := strings.ToLower(s)
	if isDNSLabel(lower) && lower == s && len(lower) <= 32 && !looksSuffixed(lower) {
		return lower // already a clean, unambiguous slug: identity, no suffix needed
	}
	var b strings.Builder
	for _, r := range lower {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	folded := strings.Trim(b.String(), "-")
	if len(folded) > 32 {
		folded = strings.Trim(folded[:32], "-")
	}
	// Disambiguate with a 64-bit hash of the RAW (unsafe-rune-free, untrimmed)
	// owner so distinct owners that fold together stay distinct. The suffix is
	// derived from the exact input bytes — NEVER a trimmed copy — so a collision
	// would need a SHA-256 collision on the owner bytes themselves.
	sum := sha256.Sum256([]byte(s))
	return folded + "-" + hex.EncodeToString(sum[:8])
}

// isDNSLabel reports whether s is a non-empty [a-z0-9-] string that is not "."
// or "..". Such an owner needs no disambiguation (sanitizeOrg is the identity on
// it), matching iam/object/orgdb.go's validateOrgSlug.
func isDNSLabel(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

// looksSuffixed reports whether s ends in "-" + exactly 16 lowercase-hex chars —
// the shape of sanitizeOrg's own disambiguation suffix. A clean slug of this
// shape is denied the identity fast-path (and re-suffixed) so it can never alias
// a folded non-slug owner's output. This is the ONLY collision class the identity
// fast-path could otherwise admit.
func looksSuffixed(s string) bool {
	if len(s) < 17 || s[len(s)-17] != '-' {
		return false
	}
	for _, r := range s[len(s)-16:] {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

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
// from name; sanitizeIdent makes the name a safe SQL/Mongo/ClickHouse
// identifier. Injective in (org,name) up to a 64-bit SHA-256 collision. With
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
var mounted *svc

// Shutdown closes the provisioning metadata store. Idempotent. Mirrors the
// plansvc Shutdown contract so the serve layer can release subsystem resources
// uniformly.
func Shutdown(context.Context) error {
	if mounted == nil || mounted.store == nil {
		return nil
	}
	err := mounted.store.Close()
	mounted = nil
	return err
}
