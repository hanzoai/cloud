package goja

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cek"
)

// tinyBundle is a self-contained goja bundle exercising the RW-Base contract:
// __db.query/exec, __newId, per-tenant orgId scoping, and a route that writes
// then throws (to prove per-request rollback).
const tinyBundle = `
globalThis.handle = function (req) {
  var org = req.orgId;
  if (req.route === 'put') {
    __db.exec('INSERT INTO kv(org,k,v,at) VALUES($1,$2,$3,$4)', [org, req.body.k, req.body.v, __now()]);
    return { status: 201, body: { ok: true, id: __newId() } };
  }
  if (req.route === 'get') {
    var rows = __db.query('SELECT k,v FROM kv WHERE org=$1 AND k=$2', [org, req.params.k]);
    if (rows.length === 0) { return { status: 404, body: {} }; }
    return { status: 200, body: rows[0] };
  }
  if (req.route === 'list') {
    return { status: 200, body: __db.query('SELECT k,v FROM kv WHERE org=$1 ORDER BY k', [org]) };
  }
  if (req.route === 'boom') {
    // caught-error path (what real bundles do): write, then return 5xx.
    __db.exec('INSERT INTO kv(org,k,v,at) VALUES($1,$2,$3,$4)', [org, 'ghost', 'x', __now()]);
    return { status: 500, body: { error: 'boom after write' } };
  }
  if (req.route === 'throw') {
    // uncaught-throw path: write, then throw (surfaces as a host error).
    __db.exec('INSERT INTO kv(org,k,v,at) VALUES($1,$2,$3,$4)', [org, 'ghost2', 'x', __now()]);
    throw new Error('raw throw after write');
  }
  if (req.route === 'bad') { return { status: 400, body: { error: 'nope' } }; }
  return { status: 404, body: {} };
};
`

func tinyMigrate(ctx context.Context, tx *sql.Tx, _ dialect.Dialect) error {
	_, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS kv(
  org TEXT NOT NULL, k TEXT NOT NULL, v TEXT NOT NULL, at BIGINT NOT NULL,
  PRIMARY KEY(org,k))`)
	return err
}

// newTinyHost runs the tiny bundle under a subsystem name hanzo-sql knows: OrgDB
// refuses any other when CLOUD_SQL_DSN is set, and the host under test is the same
// whatever bundle it carries.
func newTinyHost(t *testing.T, onOpen func(context.Context, string, *sql.DB) error) *BaseHost {
	t.Helper()
	h, err := NewBase(BaseConfig{
		Name:    "captable",
		Bundle:  []byte(tinyBundle),
		Migrate: tinyMigrate,
		DataDir: t.TempDir(),
		OnOpen:  onOpen,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func TestRoundTrip(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newTinyHost(t, nil)
		ctx := context.Background()

		// write
		resp, err := h.Dispatch(ctx, "acme", BaseRequest{Route: "put", Body: map[string]any{"k": "founder", "v": "ada"}})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Status != 201 {
			t.Fatalf("put status=%d body=%s", resp.Status, resp.Body)
		}

		// read it back (a fresh dispatch → committed data must be visible)
		resp, err = h.Dispatch(ctx, "acme", BaseRequest{Route: "get", Params: map[string]string{"k": "founder"}})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Status != 200 {
			t.Fatalf("get status=%d body=%s", resp.Status, resp.Body)
		}
		if got := string(resp.Body); got == "" || got == "{}" {
			t.Fatalf("expected row, got %s", got)
		}
		t.Logf("round-trip body: %s", resp.Body)
	})
}

func TestPerRequestRollback(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newTinyHost(t, nil)
		ctx := context.Background()

		// (a) caught-error path: write then return 500 → status >= 400 → rollback.
		resp, err := h.Dispatch(ctx, "acme", BaseRequest{Route: "boom"})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Status != 500 {
			t.Fatalf("boom status=%d, want 500", resp.Status)
		}
		resp, err = h.Dispatch(ctx, "acme", BaseRequest{Route: "get", Params: map[string]string{"k": "ghost"}})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Status != 404 {
			t.Fatalf("ghost row survived rollback: status=%d body=%s", resp.Status, resp.Body)
		}

		// (b) uncaught-throw path: surfaces as a host error, and still rolls back.
		if _, err := h.Dispatch(ctx, "acme", BaseRequest{Route: "throw"}); err == nil {
			t.Fatal("expected a host error from an uncaught JS throw")
		}
		resp, err = h.Dispatch(ctx, "acme", BaseRequest{Route: "get", Params: map[string]string{"k": "ghost2"}})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Status != 404 {
			t.Fatalf("ghost2 row survived throw-rollback: status=%d body=%s", resp.Status, resp.Body)
		}
	})
}

func TestTenantIsolation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newTinyHost(t, nil)
		ctx := context.Background()

		if _, err := h.Dispatch(ctx, "acme", BaseRequest{Route: "put", Body: map[string]any{"k": "secret", "v": "acme-only"}}); err != nil {
			t.Fatal(err)
		}
		// A different tenant must not see acme's row (separate DB file).
		resp, err := h.Dispatch(ctx, "globex", BaseRequest{Route: "get", Params: map[string]string{"k": "secret"}})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Status != 404 {
			t.Fatalf("cross-tenant read leaked: status=%d body=%s", resp.Status, resp.Body)
		}
	})
}

func TestOnOpenSeed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		seeded := map[string]bool{}
		h := newTinyHost(t, func(_ context.Context, tenant string, db *sql.DB) error {
			seeded[tenant] = true
			_, err := db.Exec(`INSERT INTO kv(org,k,v,at) VALUES($1,$2,$3,0)`, tenant, "seed", "yes")
			return err
		})
		ctx := context.Background()
		resp, err := h.Dispatch(ctx, "acme", BaseRequest{Route: "get", Params: map[string]string{"k": "seed"}})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Status != 200 {
			t.Fatalf("seed row missing: status=%d", resp.Status)
		}
		if !seeded["acme"] {
			t.Fatal("OnOpen not called")
		}
	})
}

// TestNewIDUnique proves __newId returns distinct 128-bit crypto/rand ids (no
// time/zero fallback that could collide) and surfaces its error path as a value.
func TestNewIDUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := range 10000 {
		id, err := newID()
		if err != nil {
			t.Fatalf("newID: %v", err)
		}
		if len(id) != 33 || id[0] != 'c' {
			t.Fatalf("newID shape = %q", id)
		}
		if seen[id] {
			t.Fatalf("newID collision at %d: %q", i, id)
		}
		seen[id] = true
	}
}

// TestRefusedText proves a NUL never reaches a statement on either engine: the
// request is refused with 400 whatever the bundle does with the error, and nothing
// it wrote before the refusal survives. SQLite would have stored the byte; PostgreSQL
// refuses it with an error that names nothing.
func TestRefusedText(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		h := newTinyHost(t, nil)
		ctx := context.Background()
		resp, err := h.Dispatch(ctx, "acme", BaseRequest{Route: "put", Body: map[string]any{"k": "a", "v": "x\x00y"}})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Status != 400 {
			t.Fatalf("a NUL was not refused: status=%d body=%s", resp.Status, resp.Body)
		}
		resp, err = h.Dispatch(ctx, "acme", BaseRequest{Route: "get", Params: map[string]string{"k": "a"}})
		if err != nil {
			t.Fatal(err)
		}
		if resp.Status != 404 {
			t.Fatalf("a refused request left a row: status=%d body=%s", resp.Status, resp.Body)
		}
	})
}

// TestALegacyFileStillOpens proves a tenant file the host wrote before it opened
// through the seam is the same file now: same name, same key, same rows, and the
// migration runs over it without touching what is there.
func TestALegacyFileStillOpens(t *testing.T) {
	t.Setenv(locus.Env, "")
	dir := t.TempDir()
	ns, err := principal.OrgNamespace("acme", "")
	if err != nil {
		t.Fatal(err)
	}
	old, err := cek.Open(ns, "captable", dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`CREATE TABLE kv(
  org TEXT NOT NULL, k TEXT NOT NULL, v TEXT NOT NULL, at INTEGER NOT NULL,
  PRIMARY KEY(org,k))`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(`INSERT INTO kv(org,k,v,at) VALUES('acme','founder','ada',1)`); err != nil {
		t.Fatal(err)
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}

	h, err := NewBase(BaseConfig{Name: "captable", Bundle: []byte(tinyBundle), Migrate: tinyMigrate, DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = h.Close() })
	resp, err := h.Dispatch(context.Background(), "acme", BaseRequest{Route: "get", Params: map[string]string{"k": "founder"}})
	if err != nil {
		t.Fatal(err)
	}
	var row struct{ V string }
	if resp.Status != 200 || json.Unmarshal(resp.Body, &row) != nil || row.V != "ada" {
		t.Fatalf("the legacy row is gone: status=%d body=%s", resp.Status, resp.Body)
	}
}

// TestBaseHostMoveAndSweepToSQL proves existing encrypted SQLite files opened by a
// goja BaseHost (esign, captable, dataroom, trust) move to hanzo-sql both lazily on
// first Dispatch and proactively under CLOUD_SQL_MOVE=sweep.
func TestBaseHostMoveAndSweepToSQL(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// 1. Write two org SQLite files on disk (no DSN).
	t.Setenv(locus.Env, "")
	h1, err := NewBase(BaseConfig{Name: "captable", Bundle: []byte(tinyBundle), Migrate: tinyMigrate, DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ org, v string }{{"acme", "ada"}, {"beta", "grace"}} {
		resp, err := h1.Dispatch(ctx, tc.org, BaseRequest{Route: "put", Body: map[string]any{"k": "founder", "v": tc.v}})
		if err != nil || resp.Status != 201 {
			t.Fatalf("put %s: status=%v err=%v", tc.org, resp, err)
		}
	}
	if err := h1.Close(); err != nil {
		t.Fatal(err)
	}

	// 2. Switch to PostgreSQL with CLOUD_SQL_MOVE=lazy: opening acme moves acme, leaving beta on file.
	sqltest.Server(t)
	t.Setenv(locus.MoveEnv, string(locus.Lazy))
	h2, err := NewBase(BaseConfig{Name: "captable", Bundle: []byte(tinyBundle), Migrate: tinyMigrate, DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := h2.Dispatch(ctx, "acme", BaseRequest{Route: "get", Params: map[string]string{"k": "founder"}})
	if err != nil || resp.Status != 200 {
		t.Fatalf("get acme after lazy move: resp=%v err=%v", resp, err)
	}
	var got struct{ V string }
	if json.Unmarshal(resp.Body, &got) != nil || got.V != "ada" {
		t.Fatalf("acme moved value = %s, want ada", resp.Body)
	}
	acmeNS, _ := principal.OrgNamespace("acme", "")
	betaNS, _ := principal.OrgNamespace("beta", "")
	if p, err := locus.Where(acmeNS, "captable", dir); err != nil || p != locus.OnServer {
		t.Fatalf("acme place = %v (%v), want OnServer", p, err)
	}
	if p, err := locus.Where(betaNS, "captable", dir); err != nil || p != locus.OnFile {
		t.Fatalf("beta place before sweep = %v (%v), want OnFile", p, err)
	}
	if err := h2.Close(); err != nil {
		t.Fatal(err)
	}

	// 3. Reopen with CLOUD_SQL_MOVE=sweep: beta moves proactively without any Dispatch.
	t.Setenv(locus.MoveEnv, string(locus.Sweep))
	h3, err := NewBase(BaseConfig{Name: "captable", Bundle: []byte(tinyBundle), Migrate: tinyMigrate, DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if h3.stores.swept == nil {
		t.Fatal("expected sweep goroutine to start")
	}
	<-h3.stores.swept
	if p, err := locus.Where(betaNS, "captable", dir); err != nil || p != locus.OnServer {
		t.Fatalf("beta place after sweep = %v (%v), want OnServer", p, err)
	}
	resp, err = h3.Dispatch(ctx, "beta", BaseRequest{Route: "get", Params: map[string]string{"k": "founder"}})
	if err != nil || resp.Status != 200 {
		t.Fatalf("get beta after sweep: resp=%v err=%v", resp, err)
	}
	if json.Unmarshal(resp.Body, &got) != nil || got.V != "grace" {
		t.Fatalf("beta moved value = %s, want grace", resp.Body)
	}
	if err := h3.Close(); err != nil {
		t.Fatal(err)
	}
}
