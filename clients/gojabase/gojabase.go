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
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/goja"
)

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
	// {DataDir}/{Name}/{tenantSlug}.db.
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
	// domain-free. May be nil. A key MUST NOT collide with __db/__newId/__now.
	HostFns map[string]any
}

// Host is a compiled bundle + its per-tenant Base stores. Safe for concurrent use.
type Host struct {
	name    string
	engine  *goja.Host
	stores  *stores
	hostFns map[string]any
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
	}, nil
}

// Dispatch resolves the tenant's Base/SQLite store, opens a per-request
// transaction, injects the RW-Base host globals bound to it, and calls
// globalThis.handle. It commits on a <400 non-throwing response and rolls back
// otherwise. tenant MUST be a validated principal's org (the caller resolves it,
// e.g. via clients/principal.Tenant) — the binding does not itself authenticate.
func (h *Host) Dispatch(ctx context.Context, tenant string, req Request) (*Response, error) {
	db, err := h.stores.get(ctx, tenant)
	if err != nil {
		return nil, err
	}
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
	// Extra Go-backed host capabilities (e.g. esign's __pdf). Injected after the
	// reserved db/newId/now globals; a subsystem must not shadow those.
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
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is unrecoverable; fall back to a time-seeded value
		// rather than panic inside a JS call.
		return "c" + fmt.Sprintf("%016x%016x", time.Now().UnixNano(), time.Now().UnixNano())
	}
	return "c" + hex.EncodeToString(b[:])
}
