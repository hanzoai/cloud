package provisioning

// typed.go is provisioning's TYPED half: the READS and the DELETE of every kind,
// as zip ops rather than closures.
//
// A typed op is one registry entry with N projections — the OpenAPI operation,
// the MCP tool, the CLI command and every generated SDK method all come from it.
// The seven kinds × four verbs used to be registered from a loop over `kinds`
// with a computed path, which is invisible to all four: zipdoc refuses a
// non-constant route path (it has no identity to file prose under), so a computed
// registration can never carry a doc comment even if the handler has one. That is
// why the paths below are spelled out per kind — one declaration per published
// operation, which is what the projections key on.
//
// THE CREATES ARE HERE TOO. Every POST /v1/provisioning/<kind> runs the
// pre-provision balance gate, and its 402/503 carries the fleet's NESTED
// {"error":{"code","message"}} money body — which cloud.Denied carries off a
// RETURNED error and DenyEnvelope (installed app-wide in cloud.Serve) renders, so
// a domain-shaped refusal never needed a closure to write it. The half of that
// argument that WAS load-bearing is kept in createOf: the gate runs LAST, after
// the decode, so an unfunded org sending an invalid name is told 400 rather than
// 402. typed_wire_test.go holds untypedByDesign as a closed — and currently
// EMPTY — list, so a route registered here without a registry entry goes red.

import (
	"context"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/apps/principal"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and each In/Out field into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by `make describe`.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ----- input + view types ---------------------------------------------------

// noInput is the In of an op addressed entirely by the caller's principal: a
// listing takes nothing off the wire, because the org it lists is the validated
// one and never a caller-supplied field.
type noInput struct{}

// noContent is the Out of an op that answers 204 with an empty body. It is an
// ALIAS for the unnamed empty struct, not a definition: zip keys the response on
// 204 only when the Out type has no name, so a defined type here would publish
// "200 with a body" about a route that answers 204 with none.
type noContent = struct{}

// resourceRef addresses one provisioned resource by its name. The name is the
// path segment — the URL is the addressing authority — and these routes carry no
// request body at all (zip reads none for GET or DELETE), so there is nothing a
// caller could smuggle a second name in through.
type resourceRef struct {
	// Name is the resource's org-unique slug, from the path. Lower-cased and
	// trimmed before lookup, exactly as it was at create.
	Name string `json:"name"`
}

// provisionedSummary is one row of a kind's listing. It is deliberately narrower
// than provisionedResource: a listing never carries a credential, and never a
// username either.
type provisionedSummary struct {
	// ID is the resource's server-minted handle, "rs_"-prefixed.
	ID string `json:"id"`
	// Name is the org-unique slug the caller provisioned the resource under.
	Name string `json:"name"`
	// Kind is the product: sql, vector, datastore, kv, search, s3 or docdb.
	Kind string `json:"kind"`
	// Status is "ready", or "provisioning" while a dedicated instance is still
	// being materialized by the operator.
	Status string `json:"status"`
	// Host is the address that actually routes to this resource — a dedicated
	// instance's own in-cluster Service, or the public gateway for a shared one.
	// Never the internal admin address of a shared backend.
	Host string `json:"host"`
	// Port is the port a client connects to on Host.
	Port int `json:"port"`
	// CreatedAt is when the resource was provisioned, in unix seconds.
	CreatedAt int64 `json:"createdAt"`
}

// provisionedList is one org's resources of one kind, oldest first (the store
// orders by created_at, then id). Empty is an empty JSON array, never null.
type provisionedList []provisionedSummary

// provisionedResource is one resource's metadata. It never carries the
// generated password: that is returned once by the create and otherwise lives
// only in Hanzo KMS.
type provisionedResource struct {
	// ID is the resource's server-minted handle, "rs_"-prefixed.
	ID string `json:"id"`
	// Name is the org-unique slug the caller provisioned the resource under.
	Name string `json:"name"`
	// Kind is the product: sql, vector, datastore, kv, search, s3 or docdb.
	Kind string `json:"kind"`
	// Status is "ready", or "provisioning" while a dedicated instance is still
	// being materialized. A dedicated resource's status is reconciled from the
	// operator's live CR before this is answered, so it is never a stale ready.
	Status string `json:"status"`
	// Host is the address that actually routes to this resource — a dedicated
	// instance's own in-cluster Service, or the public gateway for a shared one.
	Host string `json:"host"`
	// Port is the port a client connects to on Host.
	Port int `json:"port"`
	// Username is the credential's user, for the kinds that mint one per
	// resource. Absent for a kind whose backend authenticates with a shared,
	// out-of-band key.
	Username string `json:"username,omitempty"`
	// Database is the logical database, collection, index or bucket this
	// resource resolves to on its backend.
	Database string `json:"database"`
}

// ----- tenancy --------------------------------------------------------------

// tenantOf is the validated org for a TYPED op, and it is the SAME decision the
// untyped create beside it makes: it reaches the request through cloud.Request
// and asks tenant().
//
// It cannot read principal.OrgFrom alone, and that is a WIRE fact rather than a
// preference. tenant() folds the org through namespace.Sanitize — the slug every
// physical name, bucket name and tenant namespace is keyed on, so a typed read
// that skipped the fold would look in a different bucket than the create wrote —
// and it buckets an ORG-LESS SuperAdmin under the literal "admin" org, which
// principal.OrgFrom cannot express (it refuses an empty org outright, so reading
// the tenant through it would turn that live admin bucket into a 403).
//
// ONE function for the whole typed half, so the two halves of this surface can
// never key their tenancy differently. Fails closed off the HTTP path: no
// request, no attested principal, no tenant.
func tenantOf(ctx context.Context) (string, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", principal.RefusedFrom(ctx)
	}
	org, ok := tenant(c)
	if !ok {
		return "", principal.Refused(c)
	}
	return org, nil
}

// ops is the typed-op receiver. A TypedHandler takes no service parameter, and a
// bound METHOD is the only form cmd/zipdoc can lift prose from — a closure
// returned by a factory is a call expression with no doc comment to read, which
// is exactly how these 21 operations came to publish nothing.
type ops struct{ s *cloud.Service[state] }

// ----- create ----------------------------------------------------------------

// createOf provisions one resource of kind for the caller's org. Seven addresses
// share it, because seven addresses ARE one create: the kind decides which
// backend and nothing else about the preamble — tenancy, name validation, the
// balance gate, the dedup check and the debit are the same code seven times over.
//
// So the SEVEN doc comments below say only what a kind gives you, and everything
// true of all seven is stated ONCE as field prose on provisionRequest and
// provisionResult — which zipdoc lifts from the shared types and the generator
// publishes on every one of the seven. One statement, seven renderings, rather
// than seven accounts of one handler.
//
// THE GATE IS THE LAST CHECK BEFORE THE FIRST WRITE, and that ordering is load
// bearing. Lifting it into middleware would run it ahead of the body decode, so
// an unfunded org sending an invalid name would be told it cannot pay for a
// request that was never valid. The refusal is cloud.Denied, which carries the
// fleet-wide {"error":{"code","message"}} money contract off a returned error.
func (o ops) createOf(ctx context.Context, kind string, in *provisionRequest) (*provisionResult, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, principal.RefusedFrom(ctx)
	}

	name := strings.ToLower(strings.TrimSpace(in.Name))
	if !nameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("name must match ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
	}
	// instance keys a k8s Secret name (<instance>-addons); constrain it to the
	// same DNS/identifier-safe slug as name so it can never inject a malformed or
	// path-traversing Secret reference. Empty is allowed (no binding).
	instance := strings.ToLower(strings.TrimSpace(in.Instance))
	if instance != "" && !nameRE.MatchString(instance) {
		return nil, zip.ErrBadRequest("instance must match ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
	}

	// Honest availability gate (now empty, kept as the mechanism). Refuse a gated
	// kind BEFORE billing or any write, so a customer is never handed a
	// cross-tenant capability nor charged for a resource we will not create. A
	// dedicated kind is never in this map.
	if reason, gated := unavailableKinds[kind]; gated {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%s", reason)
	}

	fee := cloud.ResourceFeeCents(provisionFeeEnvPrefix, kind)
	project, projectValidated := principal.ValidatedProject(c)
	if err := o.s.Bill.Gate(ctx, principal.Ledger(c), project, projectValidated, kind, fee); err != nil {
		return nil, cloud.Denied(err)
	}

	// Fast duplicate check (the UNIQUE index is the authoritative guard).
	if _, err := o.s.State.store.Get(ctx, org, kind, name); err == nil {
		return nil, zip.ErrConflict("resource already exists")
	} else if !errors.Is(err, errNotFound) {
		return nil, zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}

	// DEDICATED-instance strategy (sql, kv, docdb, datastore): the org's OWN
	// isolated instance, launched via an operator Datastore CR in tenant-<org>.
	if e, dedicated := dedicatedEngines[kind]; dedicated {
		return createDedicated(o.s, c, ctx, kind, org, name, e, fee, instance)
	}
	return o.createShared(ctx, c, kind, org, name, project, fee)
}

// createShared is the SHARED-logical strategy: a logical resource inside an
// already-live product backend (vector, search, s3), namespaced by a fixed-width
// org hash so two tenants can never fold onto one backend resource.
func (o ops) createShared(ctx context.Context, c *zip.Ctx, kind, org, name, project string, fee int64) (*provisionResult, error) {
	// SHARED-logical strategy (vector, search, s3).
	prov := o.s.State.reg[kind]
	if prov == nil {
		return nil, zip.Errorf(http.StatusNotImplemented, "kind %q not supported", kind)
	}
	physical := physicalName(org, name)

	// Global uniqueness guard (across ALL orgs/kinds). The fixed-width org
	// hash already makes a cross-tenant fold cryptographically negligible;
	// this check plus the UNIQUE(physical_name) index make any residual fold
	// (or hash collision) FAIL CLOSED with 409 BEFORE the backend is touched
	// — never a silent shared resource, which on KV would be a cross-tenant
	// credential takeover (idempotent ACL SETUSER overwriting another
	// tenant's user) and elsewhere a cross-tenant DoS / existence oracle.
	if exists, err := o.s.State.store.PhysicalExists(ctx, physical); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	} else if exists {
		return nil, zip.ErrConflict("resource already exists")
	}

	user := physical
	pw, err := genToken(24)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}

	cs, host, port, db, err := prov.Create(ctx, physical, user, pw)
	if err != nil {
		if errors.Is(err, errAlreadyExists) {
			return nil, zip.ErrConflict("resource already exists")
		}
		o.s.Log.Error("provision failed", "kind", kind, "org", org, "name", name, "err", err)
		return nil, zip.Errorf(http.StatusBadGateway, "provision %s failed: %v", kind, err)
	}

	// Secret handling. Only secretful kinds carry a real per-resource
	// password. Seal it in KMS when configured; otherwise return once and
	// store nothing (never plaintext).
	secretRef := fmt.Sprintf("orgs/%s/%s/%s", org, kind, name)
	storedRef, returnPw, username := "", "", ""
	if secretfulKinds[kind] {
		returnPw, username = pw, user
		if o.s.State.sec.Enabled() {
			if err := o.s.State.sec.Put(secretRef, []byte(pw)); err != nil {
				_ = prov.Drop(ctx, physical, user)
				o.s.Log.Error("kms put failed; rolled back backend", "kind", kind, "err", err)
				return nil, zip.Errorf(http.StatusInternalServerError, "store secret failed")
			}
			storedRef = secretRef
		} else {
			o.s.Log.Warn("KMS degraded: password returned once, not persisted", "kind", kind, "org", org, "name", name)
		}
	}

	id, err := genID()
	if err != nil {
		_ = prov.Drop(ctx, physical, user)
		if storedRef != "" {
			_ = o.s.State.sec.Delete(storedRef)
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}

	r := Resource{
		ID: id, Org: org, Kind: kind, Name: name,
		PhysicalName: physical, SecretRef: storedRef,
		Host: host, Port: port, Username: username, DBName: db,
		Status: "ready", CreatedAt: time.Now().Unix(),
	}
	if err := o.s.State.store.Insert(ctx, r); err != nil {
		// Lost a concurrent race or DB error — undo the backend + secret.
		_ = prov.Drop(ctx, physical, user)
		if storedRef != "" {
			_ = o.s.State.sec.Delete(storedRef)
		}
		if errors.Is(err, errConflict) {
			return nil, zip.ErrConflict("resource already exists")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}

	// Resource is live + persisted — debit the caller's org ledger for the
	// provision (per-org, env-attributed, async best-effort so the debit never
	// blocks or corrupts this 201; a debit failure is logged for
	// reconciliation). Recurring storage footprint reuses o.s.Bill.Meter with a
	// GB-month amount once a live-size source exists.
	o.s.Bill.Meter(principal.Ledger(c), project, kind, fee, c.RequestID(), cloud.ClientIP(c))

	// Return the PUBLIC endpoint, never the internal admin host. Remap the
	// connection string's host:port too so a copy-pasted DSN is routable.
	ph, pp := publicEndpoint(kind)
	pubCS := cs
	if cs != "" {
		pubCS = strings.ReplaceAll(cs, fmt.Sprintf("%s:%d", host, port), fmt.Sprintf("%s:%d", ph, pp))
	}
	return &provisionResult{
		ID: id, Kind: kind, Name: name, Status: "ready",
		Host: ph, Port: pp, Username: username, Database: db,
		ConnectionString: pubCS, Password: returnPw,
	}, nil
}

// ----- the three shared cores ------------------------------------------------

// listOf answers one kind's listing for the caller's org. Never a password.
func (o ops) listOf(ctx context.Context, kind string) (*provisionedList, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.List(ctx, org, kind)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make(provisionedList, 0, len(rows))
	for _, r := range rows {
		host, port := endpointFor(r)
		out = append(out, provisionedSummary{
			ID: r.ID, Name: r.Name, Kind: r.Kind, Status: r.Status,
			Host: host, Port: port, CreatedAt: r.CreatedAt,
		})
	}
	return &out, nil
}

// viewOf answers one resource's metadata, reconciling a dedicated instance's
// readiness from the operator's live CR first. Never a password.
func (o ops) viewOf(ctx context.Context, kind, name string) (*provisionedResource, error) {
	org, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	name = strings.ToLower(strings.TrimSpace(name))
	r, err := o.s.State.store.Get(ctx, org, kind, name)
	if errors.Is(err, errNotFound) {
		return nil, zip.ErrNotFound("resource not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "get: %v", err)
	}
	// For a dedicated instance, reconcile provisioning -> ready from the
	// operator's live CR status before answering (honest readiness).
	if _, dedicated := dedicatedEngines[r.Kind]; dedicated {
		r = reconcileDedicated(o.s, ctx, r)
	}
	host, port := endpointFor(r)
	return &provisionedResource{
		ID: r.ID, Name: r.Name, Kind: r.Kind, Status: r.Status,
		Host: host, Port: port, Username: r.Username, Database: r.DBName,
	}, nil
}

// dropOf deprovisions the backend resource, deletes the sealed secret, and
// removes the metadata row. Returns nil on success, which zip answers 204.
func (o ops) dropOf(ctx context.Context, kind, name string) error {
	org, err := tenantOf(ctx)
	if err != nil {
		return err
	}
	name = strings.ToLower(strings.TrimSpace(name))
	r, err := o.s.State.store.Get(ctx, org, kind, name)
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
		if err := removeAddonURL(o.s, ctx, org, r.Instance, kind); err != nil {
			o.s.Log.Error("revert instance to base failed", "kind", kind, "org", org, "name", name, "instance", r.Instance, "err", err)
			return zip.Errorf(http.StatusBadGateway, "revert instance: %v", err)
		}
		// Tear down the org's dedicated instance (CR + admin Secret); the
		// operator GCs the StatefulSet + Service + PVC. Removing the row below
		// also stops the recurring footprint meter for this instance.
		if err := dropDedicated(o.s, ctx, r); err != nil {
			o.s.Log.Error("deprovision instance failed", "kind", kind, "org", org, "name", name, "err", err)
			return zip.Errorf(http.StatusBadGateway, "deprovision %s failed: %v", kind, err)
		}
	} else if prov := o.s.State.reg[kind]; prov != nil {
		if err := prov.Drop(ctx, r.PhysicalName, r.Username); err != nil {
			o.s.Log.Error("deprovision failed", "kind", kind, "org", org, "name", name, "err", err)
			return zip.Errorf(http.StatusBadGateway, "deprovision %s failed: %v", kind, err)
		}
	}
	if r.SecretRef != "" {
		if err := o.s.State.sec.Delete(r.SecretRef); err != nil {
			o.s.Log.Warn("kms delete failed (continuing)", "ref", r.SecretRef, "err", err)
		}
	}
	if _, err := o.s.State.store.Delete(ctx, org, kind, name); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "delete: %v", err)
	}
	return nil
}

// ----- sql: Hanzo SQL, a dedicated PostgreSQL instance ----------------------

// ListSQL lists the caller org's Hanzo SQL databases. Each one is a DEDICATED
// PostgreSQL instance the org alone runs, so the host is that instance's own
// in-cluster Service and the port is 5432.
func (o ops) listSQL(ctx context.Context, _ *noInput) (*provisionedList, error) {
	return o.listOf(ctx, "sql")
}

// GetSQL returns one Hanzo SQL database's metadata. It carries the database's
// status, its instance address and the admin user Postgres booted with — never
// the password, which is returned once at create and otherwise lives only in
// Hanzo KMS. A still-booting instance reads "provisioning", reconciled from the
// operator's live view rather than from the row.
//
// Example: {"name": "orders"}
func (o ops) getSQL(ctx context.Context, in *resourceRef) (*provisionedResource, error) {
	return o.viewOf(ctx, "sql", in.Name)
}

// DropSQL deprovisions one Hanzo SQL database. It reverts any app instance
// bound to it back to Base BEFORE tearing down the org's dedicated Postgres
// instance — never a live app pointed at a deleted backend — then deletes the
// sealed credential and removes the metadata row. Answers 204 with no body; a
// second call is a 404, not a second delete.
//
// Example: {"name": "orders"}
func (o ops) dropSQL(ctx context.Context, in *resourceRef) (*noContent, error) {
	return nil, o.dropOf(ctx, "sql", in.Name)
}

// ----- kv: Hanzo KV, a dedicated Valkey instance ----------------------------

// ListKV lists the caller org's Hanzo KV stores. Each one is a DEDICATED Valkey
// instance the org alone runs, so the host is that instance's own in-cluster
// Service and the port is 6379.
func (o ops) listKV(ctx context.Context, _ *noInput) (*provisionedList, error) {
	return o.listOf(ctx, "kv")
}

// GetKV returns one Hanzo KV store's metadata. It carries the store's status,
// its instance address and the Valkey user it authenticates as ("default", the
// only user a requirepass instance has) — never the password. A still-booting
// instance reads "provisioning", reconciled from the operator's live view.
//
// Example: {"name": "sessions"}
func (o ops) getKV(ctx context.Context, in *resourceRef) (*provisionedResource, error) {
	return o.viewOf(ctx, "kv", in.Name)
}

// DropKV deprovisions one Hanzo KV store. It reverts any app instance bound to
// it back to Base BEFORE tearing down the org's dedicated Valkey instance, then
// deletes the sealed credential and removes the metadata row. Answers 204 with
// no body; a second call is a 404.
//
// Example: {"name": "sessions"}
func (o ops) dropKV(ctx context.Context, in *resourceRef) (*noContent, error) {
	return nil, o.dropOf(ctx, "kv", in.Name)
}

// ----- datastore: Hanzo Datastore, a dedicated analytical instance ----------

// ListDatastore lists the caller org's Hanzo Datastore warehouses. Each one is
// a DEDICATED analytical instance the org alone runs, so the host is that
// instance's own in-cluster Service and the port is its HTTP port, 8123.
func (o ops) listDatastore(ctx context.Context, _ *noInput) (*provisionedList, error) {
	return o.listOf(ctx, "datastore")
}

// GetDatastore returns one Hanzo Datastore warehouse's metadata. It carries the
// warehouse's status, its instance address and the admin user the instance
// booted with — never the password. A still-booting instance reads
// "provisioning", reconciled from the operator's live view rather than the row.
//
// Example: {"name": "warehouse"}
func (o ops) getDatastore(ctx context.Context, in *resourceRef) (*provisionedResource, error) {
	return o.viewOf(ctx, "datastore", in.Name)
}

// DropDatastore deprovisions one Hanzo Datastore warehouse. It reverts any app
// instance bound to it back to Base BEFORE tearing down the org's dedicated
// instance, then deletes the sealed credential and removes the metadata row.
// Answers 204 with no body; a second call is a 404.
//
// Example: {"name": "warehouse"}
func (o ops) dropDatastore(ctx context.Context, in *resourceRef) (*noContent, error) {
	return nil, o.dropOf(ctx, "datastore", in.Name)
}

// ----- docdb: Hanzo DocDB, a dedicated MongoDB-wire instance ----------------

// ListDocDB lists the caller org's Hanzo DocDB document databases. Each one is
// a DEDICATED FerretDB instance the org alone runs, speaking the MongoDB wire
// protocol, so the host is that instance's own in-cluster Service and the port
// is 27017.
func (o ops) listDocDB(ctx context.Context, _ *noInput) (*provisionedList, error) {
	return o.listOf(ctx, "docdb")
}

// GetDocDB returns one Hanzo DocDB database's metadata. It carries the
// database's status, its instance address and the SCRAM user the instance was
// set up with — never the password. A still-booting instance reads
// "provisioning", reconciled from the operator's live view.
//
// Example: {"name": "sessions"}
func (o ops) getDocDB(ctx context.Context, in *resourceRef) (*provisionedResource, error) {
	return o.viewOf(ctx, "docdb", in.Name)
}

// DropDocDB deprovisions one Hanzo DocDB database. It reverts any app instance
// bound to it back to Base BEFORE tearing down the org's dedicated FerretDB
// instance, then deletes the sealed credential and removes the metadata row.
// Answers 204 with no body; a second call is a 404.
//
// Example: {"name": "sessions"}
func (o ops) dropDocDB(ctx context.Context, in *resourceRef) (*noContent, error) {
	return nil, o.dropOf(ctx, "docdb", in.Name)
}

// ----- vector: a collection on the shared vector backend --------------------

// ListVector lists the caller org's vector collections. A collection is a
// logical resource inside an already-live shared backend, so every one of them
// is reached through the public gateway rather than at an instance of its own.
func (o ops) listVector(ctx context.Context, _ *noInput) (*provisionedList, error) {
	return o.listOf(ctx, "vector")
}

// GetVector returns one vector collection's metadata. It carries the
// collection's status and the gateway address it is reached at, and no username:
// the backend authenticates with a shared, out-of-band key rather than a
// per-collection credential, so there is no per-resource user to report.
//
// Example: {"name": "embeddings"}
func (o ops) getVector(ctx context.Context, in *resourceRef) (*provisionedResource, error) {
	return o.viewOf(ctx, "vector", in.Name)
}

// DropVector deletes one vector collection from the shared backend and removes
// its metadata row. Answers 204 with no body; a second call is a 404.
//
// Example: {"name": "embeddings"}
func (o ops) dropVector(ctx context.Context, in *resourceRef) (*noContent, error) {
	return nil, o.dropOf(ctx, "vector", in.Name)
}

// ----- search: an index on the shared search backend ------------------------

// ListSearch lists the caller org's search indexes. An index is a logical
// resource inside an already-live shared backend, so every one of them is
// reached through the public gateway rather than at an instance of its own.
func (o ops) listSearch(ctx context.Context, _ *noInput) (*provisionedList, error) {
	return o.listOf(ctx, "search")
}

// GetSearch returns one search index's metadata. It carries the index's status
// and the gateway address it is reached at, and no username: the backend
// authenticates with a shared, out-of-band key rather than a per-index
// credential.
//
// Example: {"name": "products"}
func (o ops) getSearch(ctx context.Context, in *resourceRef) (*provisionedResource, error) {
	return o.viewOf(ctx, "search", in.Name)
}

// DropSearch deletes one search index from the shared backend and removes its
// metadata row. Answers 204 with no body; a second call is a 404.
//
// Example: {"name": "products"}
func (o ops) dropSearch(ctx context.Context, in *resourceRef) (*noContent, error) {
	return nil, o.dropOf(ctx, "search", in.Name)
}

// ----- s3: a bucket on the shared object store ------------------------------

// ListS3 lists the caller org's object-storage buckets. A bucket lives in an
// already-live shared object store and is reached through the public gateway.
// The names here are the friendly ones the org provisioned; the physical bucket
// is org-namespaced underneath, which is what keeps two tenants' buckets
// distinct.
func (o ops) listS3(ctx context.Context, _ *noInput) (*provisionedList, error) {
	return o.listOf(ctx, "s3")
}

// GetS3 returns one bucket's metadata. It carries the bucket's status and the
// gateway address it is reached at, and no username: the object store
// authenticates with a shared, out-of-band key rather than a per-bucket
// credential.
//
// Example: {"name": "uploads"}
func (o ops) getS3(ctx context.Context, in *resourceRef) (*provisionedResource, error) {
	return o.viewOf(ctx, "s3", in.Name)
}

// DropS3 deletes one bucket from the shared object store and removes its
// metadata row. Answers 204 with no body; a second call is a 404.
//
// Example: {"name": "uploads"}
func (o ops) dropS3(ctx context.Context, in *resourceRef) (*noContent, error) {
	return nil, o.dropOf(ctx, "s3", in.Name)
}

// ----- registration ---------------------------------------------------------

// mountTyped registers the typed half on the app's op registry. The paths are
// spelled out per kind rather than composed in a loop, because a computed path is
// not a constant and zipdoc refuses one: an op with no constant path has no
// identity to file its prose under, so the doc comments above would reach neither
// the document nor the MCP tool list.
//
// Registered on the *zip.App with absolute paths. cloud.Bridge is already
// installed app-wide by Serve, ahead of every mount, which is what parks the
// request these ops resolve their tenant from.
//
// The last two are the OPERATOR's, not a tenant's: they read the shared vector
// backend whole, so they sit at /v1/admin/<name> where the public projection
// drops them by address (inventory.go says why).
// createFor is the create for one kind BY NAME. It exists for a caller that holds
// the kind as a value rather than as a call site — the test harness mounting one
// kind's surface — and it is the same createOf the seven ops below reach, so a
// harness can never exercise a path the binary does not serve.
func (o ops) createFor(kind string) func(context.Context, *provisionRequest) (*provisionResult, error) {
	return func(ctx context.Context, in *provisionRequest) (*provisionResult, error) {
		return o.createOf(ctx, kind, in)
	}
}

// ── the seven creates ────────────────────────────────────────────────────────
//
// Each says only what its kind GIVES you. Everything true of all seven — the name
// slug, the instance binding, the credential returned exactly once, the balance
// gated before anything is built — is field prose on provisionRequest and
// provisionResult, stated once and published on all seven by the generator.

// CreateSQL launches your org's OWN PostgreSQL instance and answers with its
// `postgres://` connection string.
//
// The instance is yours alone — a deployment in your own tenant namespace, so its
// admin credential is naturally scoped to you and no other tenant shares the
// process. Off-cluster, where there is no orchestrator to launch one, this fails
// closed with 503 rather than handing back a shared one.
func (o ops) createSQL(ctx context.Context, in *provisionRequest) (*provisionResult, error) {
	return o.createOf(ctx, "sql", in)
}

// CreateKV launches your org's OWN key-value instance and answers with its `kv://`
// connection string.
//
// The instance is yours alone — a deployment in your own tenant namespace, so its
// admin credential is naturally scoped to you and no other tenant shares the
// process. Off-cluster this fails closed with 503 rather than handing back a
// shared one.
func (o ops) createKV(ctx context.Context, in *provisionRequest) (*provisionResult, error) {
	return o.createOf(ctx, "kv", in)
}

// CreateDocDB launches your org's OWN document-database instance and answers with
// its `mongodb://` connection string. It speaks the MongoDB wire protocol, so
// existing MongoDB drivers connect unchanged.
//
// The instance is yours alone — a deployment in your own tenant namespace, so its
// admin credential is naturally scoped to you and no other tenant shares the
// process. Off-cluster this fails closed with 503 rather than handing back a
// shared one.
func (o ops) createDocDB(ctx context.Context, in *provisionRequest) (*provisionResult, error) {
	return o.createOf(ctx, "docdb", in)
}

// CreateDatastore launches your org's OWN Hanzo Datastore instance and answers
// with its `datastore://` connection string.
//
// The instance is yours alone — a deployment in your own tenant namespace, so its
// admin credential is naturally scoped to you and no other tenant shares the
// process. Off-cluster this fails closed with 503 rather than handing back a
// shared one.
func (o ops) createDatastore(ctx context.Context, in *provisionRequest) (*provisionResult, error) {
	return o.createOf(ctx, "datastore", in)
}

// CreateVector creates a vector collection inside the already-running shared
// vector backend and answers with the endpoint that reaches it.
func (o ops) createVector(ctx context.Context, in *provisionRequest) (*provisionResult, error) {
	return o.createOf(ctx, "vector", in)
}

// CreateSearch creates a search index inside the already-running shared search
// backend and answers with the endpoint that reaches it.
func (o ops) createSearch(ctx context.Context, in *provisionRequest) (*provisionResult, error) {
	return o.createOf(ctx, "search", in)
}

// CreateS3 creates an S3-compatible bucket inside the already-running shared
// object store and answers with the endpoint that reaches it.
func (o ops) createS3(ctx context.Context, in *provisionRequest) (*provisionResult, error) {
	return o.createOf(ctx, "s3", in)
}

func mountTyped(z *zip.App, o ops) {
	created := zip.WithStatus(http.StatusCreated)

	zip.Post(z, "/v1/provisioning/sql", o.createSQL, created)
	zip.Post(z, "/v1/provisioning/kv", o.createKV, created)
	zip.Post(z, "/v1/provisioning/docdb", o.createDocDB, created)
	zip.Post(z, "/v1/provisioning/datastore", o.createDatastore, created)
	zip.Post(z, "/v1/provisioning/vector", o.createVector, created)
	zip.Post(z, "/v1/provisioning/search", o.createSearch, created)
	zip.Post(z, "/v1/provisioning/s3", o.createS3, created)

	zip.Get(z, "/v1/provisioning/sql", o.listSQL)
	zip.Get(z, "/v1/provisioning/sql/:name", o.getSQL)
	zip.Delete(z, "/v1/provisioning/sql/:name", o.dropSQL)

	zip.Get(z, "/v1/provisioning/kv", o.listKV)
	zip.Get(z, "/v1/provisioning/kv/:name", o.getKV)
	zip.Delete(z, "/v1/provisioning/kv/:name", o.dropKV)

	zip.Get(z, "/v1/provisioning/datastore", o.listDatastore)
	zip.Get(z, "/v1/provisioning/datastore/:name", o.getDatastore)
	zip.Delete(z, "/v1/provisioning/datastore/:name", o.dropDatastore)

	zip.Get(z, "/v1/provisioning/docdb", o.listDocDB)
	zip.Get(z, "/v1/provisioning/docdb/:name", o.getDocDB)
	zip.Delete(z, "/v1/provisioning/docdb/:name", o.dropDocDB)

	zip.Get(z, "/v1/provisioning/vector", o.listVector)
	zip.Get(z, "/v1/provisioning/vector/:name", o.getVector)
	zip.Delete(z, "/v1/provisioning/vector/:name", o.dropVector)

	zip.Get(z, "/v1/provisioning/search", o.listSearch)
	zip.Get(z, "/v1/provisioning/search/:name", o.getSearch)
	zip.Delete(z, "/v1/provisioning/search/:name", o.dropSearch)

	zip.Get(z, "/v1/provisioning/s3", o.listS3)
	zip.Get(z, "/v1/provisioning/s3/:name", o.getS3)
	zip.Delete(z, "/v1/provisioning/s3/:name", o.dropS3)

	zip.Get(z, "/v1/admin/provisioning/vector/collections", o.adminVectorCollections)
	zip.Get(z, "/v1/admin/provisioning/vector/stats", o.adminVectorStats)
}
