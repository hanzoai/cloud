package gojabase

import (
	"context"
	"database/sql"
	"testing"
)

// tinyBundle is a self-contained goja bundle exercising the RW-Base contract:
// __db.query/exec, __newId, per-tenant orgId scoping, and a route that writes
// then throws (to prove per-request rollback).
const tinyBundle = `
globalThis.handle = function (req) {
  var org = req.orgId;
  if (req.route === 'put') {
    __db.exec('INSERT INTO kv(org,k,v,at) VALUES(?,?,?,?)', [org, req.body.k, req.body.v, __now()]);
    return { status: 201, body: { ok: true, id: __newId() } };
  }
  if (req.route === 'get') {
    var rows = __db.query('SELECT k,v FROM kv WHERE org=? AND k=?', [org, req.params.k]);
    return { status: rows.length ? 200 : 404, body: rows[0] || {} };
  }
  if (req.route === 'list') {
    return { status: 200, body: __db.query('SELECT k,v FROM kv WHERE org=? ORDER BY k', [org]) };
  }
  if (req.route === 'boom') {
    // caught-error path (what real bundles do): write, then return 5xx.
    __db.exec('INSERT INTO kv(org,k,v,at) VALUES(?,?,?,?)', [org, 'ghost', 'x', __now()]);
    return { status: 500, body: { error: 'boom after write' } };
  }
  if (req.route === 'throw') {
    // uncaught-throw path: write, then throw (surfaces as a host error).
    __db.exec('INSERT INTO kv(org,k,v,at) VALUES(?,?,?,?)', [org, 'ghost2', 'x', __now()]);
    throw new Error('raw throw after write');
  }
  if (req.route === 'bad') { return { status: 400, body: { error: 'nope' } }; }
  return { status: 404, body: {} };
};
`

const tinySchema = `CREATE TABLE IF NOT EXISTS kv(
  org TEXT NOT NULL, k TEXT NOT NULL, v TEXT NOT NULL, at INTEGER NOT NULL,
  PRIMARY KEY(org,k));`

func newTinyHost(t *testing.T, onOpen func(context.Context, string, *sql.DB) error) *Host {
	t.Helper()
	h, err := New(Config{
		Name:    "tiny",
		Bundle:  []byte(tinyBundle),
		Schema:  tinySchema,
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
	h := newTinyHost(t, nil)
	ctx := context.Background()

	// write
	resp, err := h.Dispatch(ctx, "acme", Request{Route: "put", Body: map[string]any{"k": "founder", "v": "ada"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 201 {
		t.Fatalf("put status=%d body=%s", resp.Status, resp.Body)
	}

	// read it back (a fresh dispatch → committed data must be visible)
	resp, err = h.Dispatch(ctx, "acme", Request{Route: "get", Params: map[string]string{"k": "founder"}})
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
}

func TestPerRequestRollback(t *testing.T) {
	h := newTinyHost(t, nil)
	ctx := context.Background()

	// (a) caught-error path: write then return 500 → status >= 400 → rollback.
	resp, err := h.Dispatch(ctx, "acme", Request{Route: "boom"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 500 {
		t.Fatalf("boom status=%d, want 500", resp.Status)
	}
	resp, err = h.Dispatch(ctx, "acme", Request{Route: "get", Params: map[string]string{"k": "ghost"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 404 {
		t.Fatalf("ghost row survived rollback: status=%d body=%s", resp.Status, resp.Body)
	}

	// (b) uncaught-throw path: surfaces as a host error, and still rolls back.
	if _, err := h.Dispatch(ctx, "acme", Request{Route: "throw"}); err == nil {
		t.Fatal("expected a host error from an uncaught JS throw")
	}
	resp, err = h.Dispatch(ctx, "acme", Request{Route: "get", Params: map[string]string{"k": "ghost2"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 404 {
		t.Fatalf("ghost2 row survived throw-rollback: status=%d body=%s", resp.Status, resp.Body)
	}
}

func TestTenantIsolation(t *testing.T) {
	h := newTinyHost(t, nil)
	ctx := context.Background()

	if _, err := h.Dispatch(ctx, "acme", Request{Route: "put", Body: map[string]any{"k": "secret", "v": "acme-only"}}); err != nil {
		t.Fatal(err)
	}
	// A different tenant must not see acme's row (separate DB file).
	resp, err := h.Dispatch(ctx, "globex", Request{Route: "get", Params: map[string]string{"k": "secret"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 404 {
		t.Fatalf("cross-tenant read leaked: status=%d body=%s", resp.Status, resp.Body)
	}
}

func TestOnOpenSeed(t *testing.T) {
	seeded := map[string]bool{}
	h := newTinyHost(t, func(_ context.Context, tenant string, db *sql.DB) error {
		seeded[tenant] = true
		_, err := db.Exec(`INSERT INTO kv(org,k,v,at) VALUES(?,?,?,0)`, tenant, "seed", "yes")
		return err
	})
	ctx := context.Background()
	resp, err := h.Dispatch(ctx, "acme", Request{Route: "get", Params: map[string]string{"k": "seed"}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 {
		t.Fatalf("seed row missing: status=%d", resp.Status)
	}
	if !seeded["acme"] {
		t.Fatal("OnOpen not called")
	}
}

func TestSlugifyContainsTraversal(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"acme", "acme"},
		{"ACME", "acme"},
		{"../evil", ".._evil"}, // no separator + not bare "." / ".." → safe single segment
		{"a/b", "a_b"},
		{"..", "_.."},
		{"", ""},
	} {
		if got := slugify(tc.in); got != tc.want {
			t.Errorf("slugify(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}
