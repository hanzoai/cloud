// base.go is the REUSABLE read-write-Base variant of this goja host: NewBase runs
// a Hanzo subsystem's self-contained JS bundle (globalThis.handle) inside
// dop251/goja and gives that bundle PERSISTENCE over a per-tenant store (a SQLite
// file, or a schema on hanzo-sql when CLOUD_SQL_DSN is set), injected as native
// host globals. It is the storage-bearing constructor of THIS
// package — the Base binding is an OPTION: reach for New (above) for a read-only
// catalog bundle (plans/pricing), NewBase here when the bundle stores data.
//
// ONE-AND-ONLY-ONE-WAY. Any subsystem that wants "run my TS business logic in
// goja, persist to Base per tenant" uses NewBase: pass a Bundle + a per-tenant
// Schema (DDL) + the DataDir, get a *BaseHost, and Dispatch(ctx, tenant, req).
// captable is the pilot; esign (#100) and dataroom (#101) reuse it unchanged —
// the binding carries ZERO domain logic (no cap-table, no signatures, no rooms),
// only the engine (goja.Host) + the Base bridge.
//
// # Host contract (what the binding injects onto the runtime per dispatch)
//
//	globalThis.__db.query(sql, args)  -> row objects           (SELECT)
//	globalThis.__db.exec(sql, args)   -> { changes }            (INSERT/UPDATE/DELETE)
//	globalThis.__newId()              -> collision-resistant id (crypto/rand)
//	globalThis.__now()                -> unix milliseconds
//
//	globalThis.handle({ route, params, query, orgId, body }) -> { status, body }
//
// `orgId` is the tenant the caller passed to Dispatch (the validated cloud
// principal's org); the binding uses it BOTH to select the per-tenant store
// AND passes it to handle so a bundle can scope rows by it (defence in depth).
// `body` is the decoded request body; `params`/`query` are string maps.
//
// # One dialect
//
// A bundle's SQL runs on both engines, so it is written in the dialect they share
// (sdk/dialect): numbered $n placeholders in the order they first appear,
// INSERT ... ON CONFLICT, BIGINT integers, and a quoted "camelCase" alias where a
// column is renamed to one (PostgreSQL folds an unquoted name to lower case). There
// is no lastId: an id the statement must hand back is RETURNING, and bundles make
// their own ids with __newId. A bundle that is not in that dialect fails at its
// first statement on PostgreSQL, loudly.
//
// # Atomicity
//
// Each Dispatch runs handle inside ONE transaction on the tenant's store, begun
// with Dialect.BeginWrite so a request that reads and then writes is the only
// writer on that store until it ends (SQLite's one connection did this by itself).
// The transaction COMMITS iff handle returns status < 400 and does not throw;
// otherwise it ROLLS BACK.
// So a request is all-or-nothing without any JS-visible transaction API — a
// multi-statement mutation (e.g. a share transfer: delete source + insert target)
// is atomic for free, and a validation 400 leaves the DB untouched.

package goja

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"time"

	"github.com/hanzoai/cloud/sdk/dialect"
)

// BlobStore is the ONE object-storage client a bundle uses to persist large binary
// payloads OUTSIDE its per-tenant SQLite (e.g. sign's PDFs — a 32 MiB base64 blob
// in a TEXT column would bloat the tenant DB and get copied on every read). The
// cloud VFS/S3 data plane (deps.VFS) satisfies it, exactly as clients/dataroom
// already uses it for document bytes. Keys are opaque; the binding tenant-scopes them.
type BlobStore interface {
	Put(ctx context.Context, key string, payload []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
}

// BaseRequest is the dispatch envelope. The binding adds the tenant (as orgId) and
// the Base bridge; the caller supplies route/params/query/body.
type BaseRequest struct {
	Route string
	// Method is the HTTP method this dispatch stands for, passed to handle as
	// req.method. Empty leaves the bundle its own default.
	Method string
	Params map[string]string
	Query  map[string]string
	Body   any
}

// BaseConfig configures a BaseHost.
type BaseConfig struct {
	// Name identifies the subsystem ("captable", "esign", "dataroom"). It names
	// the goja host AND the per-tenant database it opens.
	Name string
	// Bundle is the self-contained goja bundle exposing globalThis.handle.
	Bundle []byte
	// Migrate makes and upgrades the tenant's tables, on every tenant store when it
	// first opens (so it is idempotent: CREATE TABLE IF NOT EXISTS, a column added
	// once behind d.HasColumn). It runs inside Dialect.Migrate: one transaction on
	// the one connection that holds the store's migration lock, so everything it does
	// goes through tx and nothing through a handle of its own. d is the engine the
	// store is on, for the few things the engines spell differently (d.Bytewise(),
	// d.Sequence).
	Migrate func(ctx context.Context, tx *sql.Tx, d dialect.Dialect) error
	// DataDir is the deployment data root; per-tenant SQLite files land where
	// namespace renders them, at {DataDir}/orgs/{slug}/{Name}.db. With CLOUD_SQL_DSN
	// set the store is a schema on hanzo-sql and DataDir is not read.
	DataDir string
	// OnOpen is an optional per-tenant seed hook run ONCE after migration (e.g.
	// captable seeds the tenant's company row). It runs outside the per-request
	// transaction, on the tenant's own handle, in the shared dialect. May be nil.
	OnOpen func(ctx context.Context, tenant string, db *sql.DB) error
	// HostFns are OPTIONAL extra native host globals injected onto the runtime on
	// every Dispatch, ALONGSIDE __db/__newId/__now — for capabilities goja cannot
	// provide that a subsystem implements in Go (e.g. esign injects __pdf =
	// { stamp, sign } for PDF rendering + x509/PKCS#7 signing). Values are Go
	// funcs or map[string]any of Go funcs (goja exposes them as callable JS). They
	// are process-global (set once at NewBase), not per-tenant; the binding stays
	// domain-free. May be nil. A key MUST NOT collide with __db/__newId/__now/__blob.
	HostFns map[string]any
	// Blob is the OPTIONAL object-storage client (see BlobStore). When set, the binding
	// injects globalThis.__blob = { put(key, b64), get(key) -> b64 } on every
	// Dispatch, bound to the tenant: keys are prefixed with {Name}/{TenantSegment}
	// so a bundle can NEVER address another tenant's blob. Payloads cross as base64
	// strings (goja-friendly); the binding decodes/encodes at the boundary so the
	// bundle never handles raw bytes. nil ⇒ no __blob is injected. This is the ONE
	// way a bundle keeps big binaries out of its per-tenant SQLite.
	Blob BlobStore
	// Bind is the TENANT-SCOPED form of HostFns: an OPTIONAL hook returning extra
	// host globals bound to the tenant of THIS dispatch. HostFns answers the same
	// way for every organization; Bind is for a capability whose answer depends on
	// who is asking — trust injects __own (is this tenant the one whose inventory
	// is compiled in) and __audit (the trail, already bound to this tenant, so the
	// bundle has no field in which to name another one).
	//
	// It takes the tenant rather than letting the bundle pass one, which is the
	// same property __blob gets from namespacing its keys host-side: a tenant the
	// bundle cannot express is a tenant the bundle cannot cross. May be nil.
	Bind func(ctx context.Context, tenant string) map[string]any
}

// BaseHost is a compiled bundle + its per-tenant Base stores. Safe for concurrent use.
type BaseHost struct {
	name    string
	engine  *Host
	stores  *stores
	hostFns map[string]any
	blob    BlobStore
	bind    func(context.Context, string) map[string]any
}

// NewBase compiles the bundle (via the goja engine, New) and prepares the
// per-tenant store manager. It does NOT open any tenant DB — those open lazily on
// first Dispatch.
func NewBase(cfg BaseConfig) (*BaseHost, error) {
	if cfg.Name == "" {
		return nil, errors.New("gojabase: Config.Name required")
	}
	if len(cfg.Bundle) == 0 {
		return nil, fmt.Errorf("gojabase[%s]: empty bundle", cfg.Name)
	}
	if cfg.DataDir == "" {
		return nil, fmt.Errorf("gojabase[%s]: Config.DataDir required", cfg.Name)
	}
	engine, err := New(Config{Name: cfg.Name, Bundle: cfg.Bundle})
	if err != nil {
		return nil, err
	}
	return &BaseHost{
		name:    cfg.Name,
		engine:  engine,
		stores:  newStores(cfg.Name, cfg.DataDir, cfg.Migrate, cfg.OnOpen),
		hostFns: cfg.HostFns,
		blob:    cfg.Blob,
		bind:    cfg.Bind,
	}, nil
}

// Dispatch resolves the tenant's Base/SQLite store, opens a per-request
// transaction, injects the RW-Base host globals bound to it, and calls
// globalThis.handle. It commits on a <400 non-throwing response and rolls back
// otherwise. tenant MUST be a validated principal's org (the caller resolves it,
// e.g. via clients/principal.Org) — the binding does not itself authenticate.
func (h *BaseHost) Dispatch(ctx context.Context, tenant string, req BaseRequest) (*Response, error) {
	db, release, err := h.stores.acquire(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer release() // unpin the pooled DB so it can be idle-evicted after the request
	tx, err := dialect.Of(db).BeginWrite(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("gojabase[%s]: begin: %w", h.name, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// A subsystem's own capabilities go in FIRST and the reserved globals go in
	// LAST, so the rule "a subsystem must not shadow db/newId/now/blob" is
	// enforced by the order rather than by a comment nobody runs. Bind beats
	// HostFns for the same reason a tenant-scoped answer beats a process-global
	// one: it knows more.
	globals := map[string]any{}
	// Extra Go-backed host capabilities, the same for every tenant (esign's __pdf).
	maps.Copy(globals, h.hostFns)
	// The tenant-scoped ones (trust's __own and __audit), which need to know who
	// is asking.
	if h.bind != nil {
		maps.Copy(globals, h.bind(ctx, tenant))
	}
	var refused error // the text a statement was given that PostgreSQL would refuse
	globals["__db"] = newBridge(ctx, tx, &refused)
	globals["__newId"] = newID
	globals["__now"] = func() int64 { return time.Now().UnixMilli() }
	// Tenant-bound object-storage client (e.g. sign's PDFs). Keys are namespaced to
	// {Name}/{TenantSegment} inside the bridge, so a bundle can only ever reach its
	// OWN tenant's blobs.
	if h.blob != nil {
		globals["__blob"] = h.blobBridge(ctx, tenant)
	}
	resp, err := h.engine.DispatchWith(ctx, Request{
		Route:  req.Route,
		Method: req.Method,
		Params: req.Params,
		Query:  req.Query,
		OrgID:  tenant,
		Body:   req.Body,
	}, globals)
	if refused != nil {
		// The bundle may have answered around the refusal, or not caught it at all; the
		// request stored nothing either way, and the caller is told why: it is theirs to fix.
		body, mErr := json.Marshal(map[string]string{"error": refused.Error()})
		if mErr != nil {
			return nil, mErr
		}
		return &Response{Status: http.StatusBadRequest, Body: body}, nil
	}
	if err != nil {
		return nil, err
	}
	if resp.Status < 400 {
		if cErr := tx.Commit(); cErr != nil {
			return nil, fmt.Errorf("gojabase[%s]: commit: %w", h.name, cErr)
		}
		committed = true
	}
	return resp, nil
}

// Tx runs fn against the tenant's OWN store under the same discipline Dispatch
// uses — the pooled handle, one transaction, commit on success and roll back on
// error — for the part of a subsystem the bundle does not own.
//
// It exists because the Go host already owns the per-tenant DDL (BaseConfig.Schema)
// while the bundle owns the routes over it, so a subsystem that grows a table with
// no counterpart in the bundle's upstream has nowhere to put the reads and writes.
// The alternative is a second opener on the same file, which is a second pool and a
// second writer against SQLite's one — so this hands back the handle the pool
// already holds rather than letting a caller open beside it.
//
// tenant MUST be a validated principal's org, exactly as for Dispatch.
func (h *BaseHost) Tx(ctx context.Context, tenant string, fn func(*sql.Tx) error) error {
	db, release, err := h.stores.acquire(ctx, tenant)
	if err != nil {
		return err
	}
	defer release()
	tx, err := dialect.Of(db).BeginWrite(ctx, db)
	if err != nil {
		return fmt.Errorf("gojabase[%s]: begin: %w", h.name, err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("gojabase[%s]: commit: %w", h.name, err)
	}
	return nil
}

// Close closes every open tenant DB and drops the goja engine. Idempotent.
func (h *BaseHost) Close() error {
	err := h.stores.closeAll()
	if h.engine != nil {
		_ = h.engine.Close()
	}
	return err
}

// execQuerier is the shared surface of *sql.Tx and *sql.DB the bridge runs on.
type execQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// newBridge builds the __db object (a map[string]any of native Go funcs that
// goja exposes as callable JS methods) bound to ctx + the per-request tx. A text
// argument PostgreSQL would refuse (a NUL, bytes that are not UTF-8) never reaches a
// statement, on either engine: the statement is not run, the error is thrown to the
// bundle, and refused keeps it so Dispatch can answer 400 whatever the bundle did.
func newBridge(ctx context.Context, q execQuerier, refused *error) map[string]any {
	checked := func(args []any) error {
		for _, a := range args {
			if s, ok := a.(string); ok {
				if err := dialect.Text(s); err != nil {
					*refused = err
					return err
				}
			}
		}
		return nil
	}
	return map[string]any{
		"query": func(query string, args []any) ([]map[string]any, error) {
			if err := checked(args); err != nil {
				return nil, err
			}
			return bridgeQuery(ctx, q, query, args)
		},
		"exec": func(query string, args []any) (map[string]any, error) {
			if err := checked(args); err != nil {
				return nil, err
			}
			return bridgeExec(ctx, q, query, args)
		},
	}
}

// blobBridge builds the __blob object (put/get over base64) bound to ctx + the
// tenant. Keys are namespaced to {name}/{TenantSegment(tenant)}/ so a bundle can
// only ever address its OWN tenant's objects — cross-tenant isolation is a host
// property, using an injective encoding of the raw org bytes. The bundle handles
// base64 strings only; the binding decodes/encodes at the boundary.
func (h *BaseHost) blobBridge(ctx context.Context, tenant string) map[string]any {
	prefix := h.name + "/" + TenantSegment(tenant) + "/"
	return map[string]any{
		"put": func(key, b64 string) error {
			raw, err := base64.StdEncoding.DecodeString(b64)
			if err != nil {
				return fmt.Errorf("gojabase[%s]: __blob.put: bad base64: %w", h.name, err)
			}
			return h.blob.Put(ctx, prefix+key, raw)
		},
		"get": func(key string) (string, error) {
			raw, err := h.blob.Get(ctx, prefix+key)
			if err != nil {
				return "", fmt.Errorf("gojabase[%s]: __blob.get: %w", h.name, err)
			}
			return base64.StdEncoding.EncodeToString(raw), nil
		},
	}
}

// bridgeQuery runs a SELECT and returns each row as a column->value object.
// TEXT columns arrive from the driver as []byte; they are normalized to string
// so the JS side sees plain strings (not byte arrays).
func bridgeQuery(ctx context.Context, q execQuerier, query string, args []any) ([]map[string]any, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, 16)
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, c := range cols {
			m[c] = normalize(vals[i])
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// bridgeExec runs an INSERT/UPDATE/DELETE and returns {changes}. There is no last
// id: PostgreSQL has none, and bundles make their own TEXT ids with __newId.
func bridgeExec(ctx context.Context, q execQuerier, query string, args []any) (map[string]any, error) {
	res, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	changes, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	return map[string]any{"changes": changes}, nil
}

// normalize converts driver-returned values to goja-friendly types: []byte
// (TEXT/BLOB) becomes string; everything else (int64, float64, bool, nil) passes
// through unchanged.
func normalize(v any) any {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return v
}

// newID returns a collision-resistant id: 'c' + 32 hex chars of crypto/rand
// (128 bits). Mirrors the shape of the cuid()s the original Prisma models used
// closely enough for external references while being cryptographically unique.
//
// A crypto/rand failure is unrecoverable and MUST NOT degrade to a predictable
// time/zero id (that would let two rows collide, or be guessed — e.g. a sign
// recipient token). It returns an error instead; goja surfaces that as a thrown
// JS Error, which fails the dispatch closed (the per-request transaction rolls
// back). It is exposed to the bundle as globalThis.__newId().
func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("gojabase: crypto/rand unavailable: %w", err)
	}
	return "c" + hex.EncodeToString(b[:]), nil
}
