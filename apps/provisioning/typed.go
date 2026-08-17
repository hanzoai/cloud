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
// THE CREATES STAY UNTYPED, and it is a wire fact, not an omission. Every
// POST /v1/<kind> runs the pre-provision balance gate and renders a denial with
// cloud.DenyResource (provisioning.go), which answers the fleet's NESTED
// {"error":{"code","message"}} at 402/503. A typed op can only refuse by
// RETURNING an error, which zip renders as its flat {"status","code","error"}
// HTTPError — errorHandler is the only path a typed op's error can take — and
// writing the nested body from inside the op does not escape it either: a nil Out
// makes zip stamp cmp.Or(op.Status, 204) over the 402 (zip typed.go:305). Moving
// the gate into middleware does not rescue it, because middleware runs BEFORE the
// body decode and would turn today's 400-on-a-bad-name into a 402. So the creates
// keep their closure, and they DECLARE their bodies through openapi.Register
// instead — the cost of staying untyped is exactly the three things zip's registry
// supplies (prose, an MCP tool, a CLI command), and not a fourth, a document that
// says the route takes no body. typed_wire_test.go holds that refusal as a
// closed list so an eighth untyped route here goes red.

import (
	"context"
	"errors"
	"github.com/hanzoai/cloud/apps/principal"
	"net/http"
	"strings"

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
// Registered on the *zip.App with absolute paths, which is the same address the
// untyped POST beside it uses — provisioning owns seven top-level nouns, not one
// prefix, so there is no group to hang them on. cloud.Bridge is already installed
// app-wide by Serve, ahead of every mount, which is what parks the request these
// ops resolve their tenant from.
func mountTyped(z *zip.App, o ops) {
	zip.Get(z, "/v1/instances/sql", o.listSQL)
	zip.Get(z, "/v1/instances/sql/:name", o.getSQL)
	zip.Delete(z, "/v1/instances/sql/:name", o.dropSQL)

	zip.Get(z, "/v1/instances/kv", o.listKV)
	zip.Get(z, "/v1/instances/kv/:name", o.getKV)
	zip.Delete(z, "/v1/instances/kv/:name", o.dropKV)

	zip.Get(z, "/v1/instances/datastore", o.listDatastore)
	zip.Get(z, "/v1/instances/datastore/:name", o.getDatastore)
	zip.Delete(z, "/v1/instances/datastore/:name", o.dropDatastore)

	zip.Get(z, "/v1/instances/docdb", o.listDocDB)
	zip.Get(z, "/v1/instances/docdb/:name", o.getDocDB)
	zip.Delete(z, "/v1/instances/docdb/:name", o.dropDocDB)

	zip.Get(z, "/v1/instances/vector", o.listVector)
	zip.Get(z, "/v1/instances/vector/:name", o.getVector)
	zip.Delete(z, "/v1/instances/vector/:name", o.dropVector)

	zip.Get(z, "/v1/instances/search", o.listSearch)
	zip.Get(z, "/v1/instances/search/:name", o.getSearch)
	zip.Delete(z, "/v1/instances/search/:name", o.dropSearch)

	zip.Get(z, "/v1/instances/s3", o.listS3)
	zip.Get(z, "/v1/instances/s3/:name", o.getS3)
	zip.Delete(z, "/v1/instances/s3/:name", o.dropS3)
}
