package datastore

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore/iface"
	"github.com/hanzoai/cloud/clients/commerce/datastore/key"
	"github.com/hanzoai/cloud/clients/commerce/datastore/query"
	"github.com/hanzoai/cloud/clients/commerce/datastore/utils"
	"github.com/hanzoai/cloud/clients/commerce/db"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

var (
	// Done signals end of iteration
	Done = errors.New("datastore: done")

	// Standard errors - aliased from db package for compatibility
	ErrNoSuchEntity          = db.ErrNoSuchEntity
	ErrInvalidEntityType     = db.ErrInvalidEntityType
	ErrInvalidKey            = db.ErrInvalidKey
	ErrConcurrentTransaction = db.ErrConcurrentModification

	// Alias utils
	IgnoreFieldMismatch = utils.IgnoreFieldMismatch

	// Global ID counter for allocations
	globalIDCounter int64 = time.Now().Unix()

	// defaultDB is the fallback database used when New() is called without an explicit db.DB.
	// Set via SetDefaultDB during application bootstrap.
	defaultDB db.DB

	// orgDBResolver resolves a per-tenant (per-org) db.DB from a namespace. It is
	// installed at bootstrap (SetOrgDBResolver = db.Manager.Org) so the merchant
	// REST datastore (NewNamespaced) physically isolates every org's rows in its
	// own store. Nil in tests/dev bootstrap → NewNamespaced falls back to the
	// shared default DB (which isolates per-org logically via the namespace column).
	orgDBResolver func(namespace string) (db.DB, error)
)

// Query is the interface for datastore queries
type Query = iface.Query

// Cursor is the interface for query cursors
type Cursor = iface.Cursor

// Datastore wraps the db.DB interface to provide the legacy API
type Datastore struct {
	Context             context.Context
	IgnoreFieldMismatch bool
	Warn                bool
	database            db.DB
	namespace           string
	allocateCounter     int64
}

// SetDefaultDB sets the global default database used by New() when no explicit db.DB is provided.
// Call this during application bootstrap after initializing the database manager.
func SetDefaultDB(database db.DB) {
	defaultDB = database
}

// SetOrgDBResolver installs the per-org database resolver used by NewNamespaced
// to route merchant-model reads/writes to the caller org's own store. Call once
// during bootstrap with db.Manager.Org. When left unset, NewNamespaced behaves
// exactly like New (shared default DB).
func SetOrgDBResolver(fn func(namespace string) (db.DB, error)) {
	orgDBResolver = fn
}

// HasOrgDBResolver reports whether the per-org resolver is installed. Bootstrap
// asserts this AFTER SetOrgDBResolver so a production binary FAILS TO START if
// the money-path resolver is missing — rather than silently letting every
// namespaced money handler fall back to the shared store (cross-tenant). The
// tests/dev nil-resolver path (NewNamespaced → shared default DB, one store per
// process) is intentional and unaffected: it is gated at Bootstrap, not here.
func HasOrgDBResolver() bool { return orgDBResolver != nil }

// NewNamespaced returns a Datastore bound to the per-org store for the namespace
// carried by ctx. This is THE merchant-model datastore: every org's product/
// order/store/customer/collection/discount/... rows live in that org's OWN
// physical store, so no request can read, list, or write another org's merchant
// data. The generic REST CRUD layer (util/rest) builds every entity through this.
//
// Fail-closed, and it preserves the global-kind path:
//   - No resolver installed (tests/dev bootstrap) OR empty namespace
//     (DefaultNamespace / global kinds such as organization, top-level user,
//     token) → the shared default DB, identical to New(ctx).
//   - Non-empty namespace with a resolver → the resolver's per-org DB. If the
//     resolver rejects the namespace (e.g. an unsafe tenant id that could escape
//     the data dir) or otherwise errors, the datastore is left with NO database,
//     so every op errors out — a namespaced request is NEVER silently served
//     from the shared/pooled store (which would cross tenants).
func NewNamespaced(ctx context.Context) *Datastore {
	d := New(ctx)
	if orgDBResolver == nil {
		return d
	}
	ns := nscontext.GetNamespace(d.Context)
	if ns == "" {
		return d
	}
	// Reserved namespaces (Red LOW-1): an untrusted tenant request (ns from the
	// verified JWT owner / X-Org-Id) must NEVER resolve to a control namespace.
	// "system" would alias the shared SQLite-fallback systemDB file
	// (Manager.Org("system")), pooling every tenant; "admin"/"default" are
	// reserved. Fail closed — NO database bound — so every op errors rather than
	// crossing into the pool. The app's OWN systemDB creation calls
	// Manager.Org("system") directly (never through NewNamespaced), so it is
	// unaffected. Case-insensitive so "System"/"ADMIN" can't slip through.
	switch strings.ToLower(ns) {
	case "system", "admin", "default":
		d.database = nil
		return d
	}
	odb, err := orgDBResolver(ns)
	if err != nil || odb == nil {
		d.database = nil
		return d
	}
	d.database = odb
	return d
}

// New creates a new Datastore with the given context.
// Uses the default database if one was set via SetDefaultDB.
//
// When called with a *gin.Context, the datastore automatically detaches
// from the HTTP request lifecycle and uses context.Background() so that
// database queries are never canceled when the browser disconnects or an
// upstream proxy timeout fires. This prevents "context canceled" errors
// across all callers without requiring each handler to be updated.
func New(ctx context.Context) *Datastore {
	// Detach from gin request context to prevent context cancellation
	// when the HTTP connection closes before the DB query completes.
	if _, ok := ctx.(*gin.Context); ok {
		ctx = context.Background()
	}

	d := new(Datastore)
	d.IgnoreFieldMismatch = true
	d.Warn = config.DatastoreWarn
	d.allocateCounter = atomic.AddInt64(&globalIDCounter, 1000)
	d.database = defaultDB
	d.SetContext(ctx)
	return d
}

// NewWithDB creates a new Datastore with a specific database
func NewWithDB(ctx context.Context, database db.DB) *Datastore {
	d := New(ctx)
	d.database = database
	return d
}

// Private method for logging selectively
func (d *Datastore) warn(fmtOrError interface{}, args ...interface{}) {
	if d.Warn {
		log.Warn(fmtOrError, args...)
	}
}

// Helper to ignore tedious field mismatch errors
func (d *Datastore) ignoreFieldMismatch(err error) error {
	if d.IgnoreFieldMismatch {
		return IgnoreFieldMismatch(err)
	}
	return err
}

// SetContext extracts the Go context from a Gin context (if applicable) and stores it.
func (d *Datastore) SetContext(ctx context.Context) {
	if c, ok := ctx.(*gin.Context); ok {
		if reqCtx := c.Value("context"); reqCtx != nil {
			if ctxVal, ok := reqCtx.(context.Context); ok {
				ctx = ctxVal
			}
		}
	}
	d.Context = ctx
}

// Set namespace for datastore
func (d *Datastore) SetNamespace(ns string) {
	d.namespace = ns
	log.Debug("Set namespace to: %s", ns)
}

// GetNamespace returns the current namespace
func (d *Datastore) GetNamespace() string {
	return d.namespace
}

// SetDB sets the underlying database
func (d *Datastore) SetDB(database db.DB) {
	d.database = database
}

// DB returns the underlying database
func (d *Datastore) DB() db.DB {
	return d.database
}

// Query returns a *datastore.Query bound to THIS datastore's own database, so a
// per-org datastore (NewNamespaced) queries its own store rather than the global
// default. For a datastore built via New() this is d.database == defaultDB, so
// the behavior is identical to the previous query.New(d.Context, kind).
func (d *Datastore) Query(kind string) Query {
	return query.NewWithDB(d.Context, kind, d.database)
}

// DecodeCursor decodes a cursor string
func (d *Datastore) DecodeCursor(cursor string) (Cursor, error) {
	return db.DecodeCursor(cursor)
}

// TransactionOptions configures transaction behavior
type TransactionOptions struct {
	// XG enables cross-group transactions (legacy compat, ignored in new db)
	XG bool

	// Attempts specifies how many retries on conflict
	Attempts int

	// ReadOnly indicates this is a read-only transaction
	ReadOnly bool
}

// RunInTransaction runs a function within a transaction
func (d *Datastore) RunInTransaction(fn func(db *Datastore) error, opts *TransactionOptions) error {
	return RunInTransaction(d.Context, fn, opts)
}

// RunInTransaction runs a function within a transaction
func RunInTransaction(ctx context.Context, fn func(db *Datastore) error, opts *TransactionOptions) error {
	// For now, just run the function directly
	// The proper implementation would use db.DB.RunInTransaction
	ds := New(ctx)
	return fn(ds)
}

// toDBKey converts a Key to db.Key
func (d *Datastore) toDBKey(k Key) db.Key {
	if k == nil {
		return nil
	}

	// If it's already a DatastoreKey, use its converter
	if dk, ok := k.(*key.DatastoreKey); ok {
		if d.database != nil {
			return dk.ToDBKey(d.database)
		}
	}

	// Create a simple wrapper
	return &dbKeyWrapper{k}
}

// dbKeyWrapper wraps iface.Key to implement db.Key
type dbKeyWrapper struct {
	key Key
}

func (w *dbKeyWrapper) Kind() string      { return w.key.Kind() }
func (w *dbKeyWrapper) StringID() string  { return w.key.StringID() }
func (w *dbKeyWrapper) IntID() int64      { return w.key.IntID() }
func (w *dbKeyWrapper) Namespace() string { return w.key.Namespace() }
func (w *dbKeyWrapper) Incomplete() bool  { return w.key.Incomplete() }
func (w *dbKeyWrapper) Encode() string    { return w.key.Encode() }

func (w *dbKeyWrapper) Parent() db.Key {
	p := w.key.Parent()
	if p == nil {
		return nil
	}
	return &dbKeyWrapper{p}
}

func (w *dbKeyWrapper) Equal(other db.Key) bool {
	if other == nil {
		return false
	}
	return w.Kind() == other.Kind() && w.Encode() == other.Encode()
}
