// Package gojabase is the REUSABLE read-write-Base goja host: it runs a Hanzo
// subsystem's self-contained JS bundle (globalThis.handle) inside dop251/goja and
// gives that bundle PERSISTENCE over per-tenant Base/SQLite, injected as native
// host globals. It is the storage-bearing sibling of clients/goja (which is the
// pure JS engine that plans/pricing use with a read-only catalog).
//
// ONE-AND-ONLY-ONE-WAY. Any subsystem that wants "run my TS business logic in
// goja, persist to Base per tenant" uses THIS package: pass a Bundle + a per-
// tenant Schema (DDL) + the DataDir, get a Host, and Dispatch(ctx, tenant, req).
// captable is the pilot; esign (#100) and dataroom (#101) reuse it unchanged —
// the binding carries ZERO domain logic (no cap-table, no signatures, no rooms),
// only the engine + the Base bridge.
//
// # Host contract (what the binding injects onto the runtime per dispatch)
//
//	globalThis.__db.query(sql, args)  -> row objects           (SELECT)
//	globalThis.__db.exec(sql, args)   -> { changes, lastId }    (INSERT/UPDATE/DELETE)
//	globalThis.__newId()              -> collision-resistant id (crypto/rand)
//	globalThis.__now()                -> unix milliseconds
//
//	globalThis.handle({ route, params, query, orgId, body }) -> { status, body }
//
// `orgId` is the tenant the caller passed to Dispatch (the validated cloud
// principal's org); the binding uses it BOTH to select the per-tenant SQLite file
// AND passes it to handle so a bundle can scope rows by it (defence in depth).
// `body` is the decoded request body; `params`/`query` are string maps.
//
// # Atomicity
//
// Each Dispatch runs handle inside ONE SQLite transaction on the tenant's DB
// (deferred BEGIN → read-only until the first write). The transaction COMMITS
// iff handle returns status < 400 and does not throw; otherwise it ROLLS BACK.
// So a request is all-or-nothing without any JS-visible transaction API — a
// multi-statement mutation (e.g. a share transfer: delete source + insert target)
// is atomic for free, and a validation 400 leaves the DB untouched.
package gojabase

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/goja"
)

// BlobStore is the ONE object-storage seam a bundle uses to persist large binary
// payloads OUTSIDE its per-tenant SQLite (e.g. sign's PDFs — a 32 MiB base64 blob
// in a TEXT column would bloat the tenant DB and get copied on every read). The
// cloud VFS/S3 data plane (deps.VFS) satisfies it, exactly as clients/dataroom
// already uses it for document bytes. Keys are opaque; gojabase tenant-scopes them.
type BlobStore interface {
	Put(ctx context.Context, key string, payload []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
}

// Response mirrors the JS-side {status, body} (reused from clients/goja).
type Response = goja.Response

// Request is the dispatch envelope. The binding adds the tenant (as orgId) and
// the Base bridge; the caller supplies route/params/query/body.
type Request struct {
	Route  string
	Params map[string]string
	Query  map[string]string
	Body   any
}

// Config configures a Host.
type Config struct {
	// Name identifies the subsystem ("captable", "esign", "dataroom"). It names
	// the goja host AND the per-tenant data subdir ({DataDir}/{Name}/).
	Name string
	// Bundle is the self-contained goja bundle exposing globalThis.handle.
	Bundle []byte
	// Schema is the per-tenant SQLite DDL, run (idempotently — use
	// CREATE TABLE IF NOT EXISTS) on every tenant DB when it first opens.
	Schema string
	// DataDir is the deployment data root; per-tenant files land at
	// {DataDir}/{Name}/{TenantSegment(tenant)}.db (injective, traversal-safe
	// base32 of the raw org bytes — see gojabase/store.go TenantSegment).
	DataDir string
	// OnOpen is an optional per-tenant seed hook run ONCE after migration (e.g.
	// captable seeds the tenant's company row). It runs outside the per-request
	// transaction. May be nil.
	OnOpen func(ctx context.Context, tenant string, db *sql.DB) error
	// HostFns are OPTIONAL extra native host globals injected onto the runtime on
	// every Dispatch, ALONGSIDE __db/__newId/__now — for capabilities goja cannot
	// provide that a subsystem implements in Go (e.g. esign injects __pdf =
	// { stamp, sign } for PDF rendering + x509/PKCS#7 signing). Values are Go
	// funcs or map[string]any of Go funcs (goja exposes them as callable JS). They
	// are process-global (set once at New), not per-tenant; the binding stays
	// domain-free. May be nil. A key MUST NOT collide with __db/__newId/__now/__blob.
	HostFns map[string]any
	// Blob is the OPTIONAL object-storage seam (see BlobStore). When set, gojabase
	// injects globalThis.__blob = { put(key, b64), get(key) -> b64 } on every
	// Dispatch, bound to the tenant: keys are prefixed with {Name}/{TenantSegment}
	// so a bundle can NEVER address another tenant's blob. Payloads cross as base64
	// strings (goja-friendly); gojabase decodes/encodes at the boundary so the
	// bundle never handles raw bytes. nil ⇒ no __blob is injected. This is the ONE
	// way a bundle keeps big binaries out of its per-tenant SQLite.
	Blob BlobStore
}

// Host is a compiled bundle + its per-tenant Base stores. Safe for concurrent use.
type Host struct {
	name    string
	engine  *goja.Host
	stores  *stores
	hostFns map[string]any
	blob    BlobStore
}

// New compiles the bundle (via clients/goja) and prepares the per-tenant store
// manager. It does NOT open any tenant DB — those open lazily on first Dispatch.
func New(cfg Config) (*Host, error) {
	if cfg.Name == "" {
		return nil, errors.New("gojabase: Config.Name required")
	}
	if len(cfg.Bundle) == 0 {
		return nil, fmt.Errorf("gojabase[%s]: empty bundle", cfg.Name)
	}
	if cfg.DataDir == "" {
		return nil, fmt.Errorf("gojabase[%s]: Config.DataDir required", cfg.Name)
	}
	engine, err := goja.New(goja.Config{Name: cfg.Name, Bundle: cfg.Bundle})
	if err != nil {
		return nil, err
	}
	return &Host{
		name:    cfg.Name,
		engine:  engine,
		stores:  newStores(cfg.Name, cfg.DataDir, cfg.Schema, cfg.OnOpen),
		hostFns: cfg.HostFns,
		blob:    cfg.Blob,
	}, nil
}

// Dispatch resolves the tenant's Base/SQLite store, opens a per-request
// transaction, injects the RW-Base host globals bound to it, and calls
// globalThis.handle. It commits on a <400 non-throwing response and rolls back
// otherwise. tenant MUST be a validated principal's org (the caller resolves it,
// e.g. via clients/principal.Org) — the binding does not itself authenticate.
func (h *Host) Dispatch(ctx context.Context, tenant string, req Request) (*Response, error) {
	db, release, err := h.stores.acquire(ctx, tenant)
	if err != nil {
		return nil, err
	}
	defer release() // unpin the pooled DB so it can be idle-evicted after the request
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("gojabase[%s]: begin: %w", h.name, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	globals := map[string]any{
		"__db":    newBridge(ctx, tx),
		"__newId": newID,
		"__now":   func() int64 { return time.Now().UnixMilli() },
	}
	// Tenant-bound object-storage seam (e.g. sign's PDFs). Keys are namespaced to
	// {Name}/{TenantSegment} inside the bridge, so a bundle can only ever reach its
	// OWN tenant's blobs — the same injective TenantSegment the per-tenant DB uses.
	if h.blob != nil {
		globals["__blob"] = h.blobBridge(ctx, tenant)
	}
	// Extra Go-backed host capabilities (e.g. esign's __pdf). Injected after the
	// reserved db/newId/now/blob globals; a subsystem must not shadow those.
	for k, v := range h.hostFns {
		globals[k] = v
	}
	resp, err := h.engine.DispatchWith(ctx, goja.Request{
		Route:  req.Route,
		Params: req.Params,
		Query:  req.Query,
		OrgID:  tenant,
		Body:   req.Body,
	}, globals)
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

// Close closes every open tenant DB and drops the goja engine. Idempotent.
func (h *Host) Close() error {
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
// goja exposes as callable JS methods) bound to ctx + the per-request tx.
func newBridge(ctx context.Context, q execQuerier) map[string]any {
	return map[string]any{
		"query": func(query string, args []any) ([]map[string]any, error) {
			return bridgeQuery(ctx, q, query, args)
		},
		"exec": func(query string, args []any) (map[string]any, error) {
			return bridgeExec(ctx, q, query, args)
		},
	}
}

// blobBridge builds the __blob object (put/get over base64) bound to ctx + the
// tenant. Keys are namespaced to {name}/{TenantSegment(tenant)}/ so a bundle can
// only ever address its OWN tenant's objects — cross-tenant isolation is a host
// property, using the same injective encoding the per-tenant DB file uses. The
// bundle handles base64 strings only; gojabase decodes/encodes at the boundary.
func (h *Host) blobBridge(ctx context.Context, tenant string) map[string]any {
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

// bridgeExec runs an INSERT/UPDATE/DELETE and returns {changes, lastId}. lastId
// is the SQLite rowid as a string (bundles generate their own TEXT ids via
// __newId and ignore it; it is returned for completeness).
func bridgeExec(ctx context.Context, q execQuerier, query string, args []any) (map[string]any, error) {
	res, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	changes, _ := res.RowsAffected()
	lastID, _ := res.LastInsertId()
	return map[string]any{"changes": changes, "lastId": fmt.Sprintf("%d", lastID)}, nil
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
