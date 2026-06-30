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
package provisioningsvc

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
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/zip"
	luxlog "github.com/luxfi/log"
)

// kinds is the closed set of resource kinds this control plane provisions.
// These strings are the Hanzo product names — never the upstream OSS name of
// the backend (so "sql"/"s3", not "postgres"/"minio").
var kinds = []string{"sql", "vector", "datastore", "kv", "search", "s3", "docdb"}

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
		return fmt.Errorf("provisioningsvc.Mount: nil zip.App")
	}
	log := deps.Logger
	if log == nil {
		return fmt.Errorf("provisioningsvc.Mount: nil deps.Logger")
	}
	log = log.New("subsystem", "provisioning")

	if deps.DataDir == "" {
		return fmt.Errorf("provisioningsvc.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("provisioningsvc.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "provisioning.db"))
	if err != nil {
		return fmt.Errorf("provisioningsvc.Mount: open store: %w", err)
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
			return fmt.Errorf("provisioningsvc.Mount: app is %T, want *zip.App", app)
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

		return c.JSON(http.StatusCreated, createResp{
			ID: id, Kind: kind, Name: name, Status: "ready",
			Host: host, Port: port, Username: username, Database: db,
			ConnectionString: cs, Password: returnPw,
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
			out = append(out, listItem{
				ID: r.ID, Name: r.Name, Kind: r.Kind, Status: r.Status,
				Host: r.Host, Port: r.Port, CreatedAt: r.CreatedAt,
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
		return c.JSON(http.StatusOK, getResp{
			ID: r.ID, Name: r.Name, Kind: r.Kind, Status: r.Status,
			Host: r.Host, Port: r.Port, Username: r.Username, Database: r.DBName,
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
// Trusting the gateway-minted X-User-IsAdmin claim (c.IsAdmin()) is acceptable
// here: the blast radius of a forged claim is bounded to the single literal
// "admin" org bucket. An admin still gets a distinct physical namespace
// ("o"<hash("admin")>_…) and cannot name into any real tenant's resources. The
// gateway strips client-supplied identity headers and only sets this claim on
// the JWT-validated path (HIP-0026), so it cannot be spoofed from the edge.
func tenant(c *zip.Ctx) (string, bool) {
	org := sanitizeOrg(c.Org())
	if org != "" {
		return org, true
	}
	if c.IsAdmin() {
		return "admin", true
	}
	return "", false
}

// sanitizeOrg reduces a gateway org id to a lowercase [a-z0-9-] slug, capped at
// 32 chars. Defense in depth: org comes from the JWT via the gateway, but it
// still flows into physical identifiers.
func sanitizeOrg(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 32 {
		out = strings.Trim(out[:32], "-")
	}
	return out
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
