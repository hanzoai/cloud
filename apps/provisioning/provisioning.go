// Package provisioning is one-click data add-ons: a SQL, key-value, document,
// vector, search or object store, wired straight into your app.
//
// It turns "create a database" into a real logical resource inside an
// already-live, shared product backend, per the unified /v1 binary (HIP-0106).
//
// One HTTP surface, seven kinds, two strategies:
//
// DEDICATED-instance — the four on-demand data add-ons. Each org OWNS its
// instance (an operator Datastore CR in tenant-<org>), so its admin credential
// is naturally tenant-scoped; the assembled DSN is injected as <KIND>_URL into
// the app instance's addons Secret, switching it off Base onto the backend:
//
//	kv        -> Hanzo KV        Datastore type=valkey       kv://…:6379
//	sql       -> Hanzo SQL       Datastore type=postgresql   postgres://…:5432
//	docdb     -> Hanzo DocDB     Datastore type=docdb        mongodb://…:27017
//	datastore -> Hanzo Datastore Datastore type=datastore    datastore://…:8123
//
// SHARED-logical — a logical resource inside an already-live shared backend:
//
//	vector    -> Qdrant      vector.hanzo.svc:6333    PUT /collections/{name}
//	search    -> Meilisearch search.hanzo.svc:7700    POST /indexes
//	s3        -> S3          s3.hanzo.svc:9000        MakeBucket
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
	"fmt"
	"github.com/hanzoai/cloud/internal/environ"
	"regexp"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"
)

// kinds is the closed set of resource kinds this control plane provisions.
// These strings are the Hanzo product names — never the upstream OSS name of
// the backend (so "sql"/"s3", not "postgres"/"seaweedfs").
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
	if h := environ.Or("PUBLIC_"+strings.ToUpper(kind)+"_HOST", ""); h != "" {
		host = h
	}
	if p := environ.Or("PUBLIC_"+strings.ToUpper(kind)+"_PORT", ""); p != "" {
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
// Ongoing storage footprint (GB-month) is billed by REUSING s.Bill.Record with a
// usage-derived amount; its unit price lives in hanzoai/pricing
// (infrastructure.blockStorage.pricePerGBMonthly = $0.08/GB-month) and is
// applied by the recurring caller, not at provision time — there is no live-size
// source here and a size is never fabricated.
const provisionFeeEnvPrefix = "CLOUD_PROVISION_FEE_CENTS"

// state is provisioning's own data; shared deps (logger, per-org billing meter,
// brand) live in the embedded cloud.Base, reached as s.Log / s.Bill. The billing
// meter is nil → Authorize allows and Record is a no-op.
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

// provisionRequest is the create body. It is a NAMED type so the seven
// POST /v1/<kind> routes — which stay untyped for the wire reason typed.go
// states — can still DECLARE the body they read through openapi.Register: an
// undeclared create publishes an SDK method with nowhere to put the name, which
// is a strictly worse document than an under-described one.
type provisionRequest struct {
	// Name is the org-unique slug for the new resource, matching
	// ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$. Every physical name derives from it.
	Name string `json:"name"`
	// Instance binds a DEDICATED add-on to the app instance whose
	// <instance>-addons Secret receives the <KIND>_URL (e.g. "commerce").
	// Optional: empty means "not instance-bound" — the DSN is returned once and
	// wired by the caller.
	Instance string `json:"instance"`
}

// provisionResult is the create response: the new resource plus the ONE-TIME
// credential. The connection string and password are returned here and nowhere
// else — the reads beside it never carry a password — so a caller that does not
// keep them must re-provision.
type provisionResult struct {
	// ID is the resource's server-minted handle, "rs_"-prefixed. The caller does
	// not choose it, and it is what every read and the delete address.
	ID string `json:"id"`
	// Kind is the product provisioned: sql, vector, datastore, kv, search, s3 or
	// docdb. It is the route that was called, not a body field.
	Kind string `json:"kind"`
	// Name is the org-unique slug the caller asked for, lower-cased. Every
	// physical name on the backend derives from it.
	Name string `json:"name"`
	// Status is "ready", or "provisioning" while a dedicated instance is still
	// being materialized by the operator. A shared-backend create is "ready" here;
	// a dedicated one answers 201 still launching, and reaches ready only when a
	// later read reconciles it against the operator's live CR — never fabricated.
	Status string `json:"status"`
	// Host is the address that routes to this resource — a dedicated instance's
	// own in-cluster Service, or the public gateway for a shared one. Never the
	// internal admin address of a shared backend.
	Host string `json:"host"`
	// Port is the port a client connects to on Host.
	Port int `json:"port"`
	// Username is the credential's user, for the kinds that mint one per resource.
	// Absent for a kind whose backend authenticates with a shared, out-of-band key.
	Username string `json:"username,omitempty"`
	// Database is the logical database, collection, index or bucket this resource
	// resolves to on its backend. It is derived from Name under an org-namespacing
	// hash, so it is not Name and two orgs cannot land on one.
	Database string `json:"database"`
	// ConnectionString is the ready-to-use DSN, credential included. RETURNED
	// HERE ONCE: no read beside this one carries it, so a caller that does not
	// keep it must provision again.
	ConnectionString string `json:"connectionString"`
	// Password is the minted credential, in plaintext, for the kinds that have
	// one. RETURNED HERE ONCE — where KMS is configured it is sealed there and
	// only a reference is persisted; where it is not, it is stored nowhere at all.
	// It is never held in plaintext on either side.
	Password string `json:"password,omitempty"`
}

// Mount wires the provisioning surface onto app per HIP-0106. It is the complex
// flavour of the generic subsystem: it keeps a package global (mounted) for
// cross-package reach and starts a recurring footprint meter, so it constructs the
// Service value directly rather than through cloud.Use.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("provisioning.Use:  nil app")
	}
	if cloud.DataDir() == "" {
		return fmt.Errorf("provisioning.Use:  empty DataDir")
	}
	// The typed half needs the op REGISTRY, not just a router: a typed op is a
	// route plus the one entry the document, the MCP tool list, the CLI and every
	// SDK are projected from. Fail the mount rather than serving 21 reads no
	// projection knows about.
	z := cloud.ZipApp(app)
	if z == nil {
		return fmt.Errorf("provisioning.Use:  %T does not expose the typed-op registry", app)
	}
	store, err := openStore(cloud.DataDir())
	if err != nil {
		return fmt.Errorf("provisioning.Use:  open store: %w", err)
	}

	b := cloud.NewBase(deps, "provisioning")
	s := &cloud.Service[state]{Base: b, State: state{
		store: store,
		sec:   newSecrets(deps.KMS, b.Log),
		reg:   newRegistry(deps),
		orch:  newOrchestrator(),
	}}
	mounted = s

	routes(z, s)

	// Recurring per-org footprint meter for running dedicated instances.
	startFootprintMeter(s)

	b.Log.Info("provisioning mounted",
		"kinds", len(kinds),
		"dedicated", len(dedicatedEngines),
		"cluster", s.State.orch != nil && s.State.orch.Ready() == nil,
		"kms", s.State.sec.Enabled(),
		"brand", cloud.Brand(),
		"env", cloud.Env(),
	)
	return nil
}

// routes registers the CRUD surface for each provisionable kind. Every verb is a
// typed op; mountTyped is the whole registration.
func routes(z *zip.App, s *cloud.Service[state]) {
	mountTyped(z, ops{s: s})
}

// create provisions a new resource of kind for the caller's org. Two strategies
// share one preamble (auth, name validation, billing gate, dedup): the shared-
// logical kinds create a resource inside a live shared backend; the dedicated
// kinds (datastore, docdb) launch the org's OWN instance (createDedicated).
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
	org := namespace.Sanitize(c.Org())
	if org != "" {
		return org, true
	}
	if c.IsAdmin() {
		return "admin", true
	}
	return "", false
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
// plan Shutdown contract so the serve layer can release subsystem resources
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
