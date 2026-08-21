package kv

// kv_test.go pins this door's wire against the REAL plane — no fakes anywhere
// in the path. The plane is apps/pubsub's embedded JetStream node, and that is
// the whole point: this app has no store of its own, so a test with a fake
// store would prove nothing about the product.
//
//   - the round trip: bucket, put/get/history revisions, tombstone reads, drop;
//   - tenancy: the org comes from the validated principal only, one org's
//     buckets never resolve for another, and the same NAME stays every org's
//     to claim;
//   - the dial: the same six ops answer with the bus reached the way this app's
//     OWN BINARY reaches it — over CLOUD_PUBSUB_URL, no in-process server —
//     because that is the arrangement the split introduced;
//   - the route oracle: every served /v1/kv operation is a typed, described op.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/pubsub"
	"github.com/hanzoai/cloud/openapi"
	psembed "github.com/hanzoai/pubsub/embed"
)

const wireTimeout = 15 * time.Second

// mount brings up the plane and this door over it, on ONE app.
//
// It calls pubsub.Mount for the server rather than reaching for a fake, and
// that is the composition the fleet ships minus a process boundary: one
// embedded node, one connection, two doors. See TestRidesThePlaneFromItsOwnBinary
// for the other half — the same door with the plane on the far side of a socket.
func mount(t *testing.T) *zip.App {
	t.Helper()
	t.Setenv("CLOUD_PUBSUB_HOST", "127.0.0.1")
	t.Setenv("CLOUD_PUBSUB_PORT", "-1") // random free port
	t.Setenv("CLOUD_PUBSUB_STORE_DIR", t.TempDir())
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	if err := pubsub.Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("pubsub.Mount: %v", err)
	}
	t.Cleanup(func() { _ = pubsub.Shutdown(context.Background()) })
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

// send issues one request with a VERBATIM body and content type.
func send(t *testing.T, app *zip.App, method, path, org, ctype, body string) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = bytes.NewReader([]byte(body))
	}
	rq := httptest.NewRequest(method, path, r)
	if ctype != "" {
		rq.Header.Set("Content-Type", ctype)
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org) // a validated principal (principal.Org gate)
	}
	resp, err := app.Test(rq, zip.TestConfig{Timeout: wireTimeout, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func doJSON(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	if body == nil {
		return send(t, app, method, path, org, "", "")
	}
	b, _ := json.Marshal(body)
	return send(t, app, method, path, org, "application/json", string(b))
}

func obj(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not an object: %v (%s)", err, raw)
	}
	return m
}

// TestRoundTrip walks keyed state through its revisions: bucket create, two
// puts, the read, the history, the tombstone, and the bucket teardown.
func TestRoundTrip(t *testing.T) {
	app := mount(t)
	const org = "org_kv"

	code, raw := doJSON(t, app, http.MethodPost, "/v1/kv/CFG", org, map[string]any{"history": 5})
	if code != http.StatusCreated {
		t.Fatalf("create bucket = %d: %s", code, raw)
	}
	if b := obj(t, raw); b["bucket"] != "CFG" || b["history"] != float64(5) {
		t.Errorf("bucket = %v, want CFG with history 5", b)
	}

	code, raw = doJSON(t, app, http.MethodPut, "/v1/kv/CFG/theme", org, map[string]any{"value": "light"})
	if code != http.StatusOK {
		t.Fatalf("put = %d: %s", code, raw)
	}
	if ack := obj(t, raw); ack["revision"] != float64(1) {
		t.Errorf("first put revision = %v, want 1", ack["revision"])
	}
	_, raw = doJSON(t, app, http.MethodPut, "/v1/kv/CFG/theme", org, map[string]any{"value": "dark"})
	if ack := obj(t, raw); ack["revision"] != float64(2) {
		t.Errorf("second put revision = %v, want 2", ack["revision"])
	}

	code, raw = send(t, app, http.MethodGet, "/v1/kv/CFG/theme", org, "", "")
	if code != http.StatusOK {
		t.Fatalf("get = %d: %s", code, raw)
	}
	if e := obj(t, raw); e["value"] != "dark" || e["revision"] != float64(2) || e["operation"] != "put" {
		t.Errorf("entry = %v, want dark at revision 2", e)
	}

	code, raw = send(t, app, http.MethodGet, "/v1/kv/CFG/theme/history", org, "", "")
	if code != http.StatusOK {
		t.Fatalf("history = %d: %s", code, raw)
	}
	if page := obj(t, raw); len(page["data"].([]any)) != 2 {
		t.Errorf("history = %v, want both revisions", page["data"])
	}

	if code, _ := send(t, app, http.MethodDelete, "/v1/kv/CFG/theme", org, "", ""); code != http.StatusNoContent {
		t.Errorf("delete key = %d, want 204", code)
	}
	if code, _ := send(t, app, http.MethodGet, "/v1/kv/CFG/theme", org, "", ""); code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404 (tombstone)", code)
	}

	if code, _ := send(t, app, http.MethodDelete, "/v1/kv/CFG", org, "", ""); code != http.StatusNoContent {
		t.Errorf("delete bucket = %d, want 204", code)
	}
	if code, _ := send(t, app, http.MethodGet, "/v1/kv/CFG/theme", org, "", ""); code != http.StatusNotFound {
		t.Errorf("get in a deleted bucket = %d, want 404", code)
	}
}

// Tenancy is a fact about the PRINCIPAL, never a field the caller can send.
func TestTenancyIsNeverACallerField(t *testing.T) {
	app := mount(t)

	if code, raw := doJSON(t, app, http.MethodPost, "/v1/kv/VAULT", "acme", nil); code != http.StatusCreated {
		t.Fatalf("seed bucket = %d: %s", code, raw)
	}
	if code, _ := doJSON(t, app, http.MethodPut, "/v1/kv/VAULT/pin", "acme", map[string]any{"value": "1234"}); code != http.StatusOK {
		t.Fatal("seed key")
	}

	// Another org: the same addresses answer 404. Writes carry a valid body so
	// the 404 is tenancy's, not a bind refusal's.
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/kv/VAULT/pin"},
		{http.MethodGet, "/v1/kv/VAULT/pin/history"},
		{http.MethodPut, "/v1/kv/VAULT/pin"},
		{http.MethodDelete, "/v1/kv/VAULT/pin"},
		{http.MethodDelete, "/v1/kv/VAULT"},
	} {
		var body any
		if tc.method == http.MethodPut {
			body = map[string]any{"value": "x"}
		}
		if code, _ := doJSON(t, app, tc.method, tc.path, "other", body); code != http.StatusNotFound {
			t.Errorf("cross-org %s %s = %d, want 404", tc.method, tc.path, code)
		}
	}

	// And the same name is another org's to claim, not a collision.
	if code, _ := doJSON(t, app, http.MethodPost, "/v1/kv/VAULT", "other", nil); code != http.StatusCreated {
		t.Errorf("the same bucket name in another org = %d, want its own 201", code)
	}

	// A name that cannot decode to one (org, bucket) pair is refused: 400 on a
	// create, where the caller is choosing the name, and 404 everywhere else,
	// where answering "malformed" would be answering about another org's plane.
	if code, _ := doJSON(t, app, http.MethodPost, "/v1/kv/has-a-dash", "acme", nil); code != http.StatusBadRequest {
		t.Errorf("create with a dash = %d, want 400 — the physical name would not decode", code)
	}
	if code, _ := send(t, app, http.MethodGet, "/v1/kv/has-a-dash/pin", "acme", "", ""); code != http.StatusNotFound {
		t.Errorf("read with a dash = %d, want 404", code)
	}

	// No principal: every op is a 403, before any bus work.
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/kv/VAULT"},
		{http.MethodDelete, "/v1/kv/VAULT"},
		{http.MethodGet, "/v1/kv/VAULT/pin"},
		{http.MethodPut, "/v1/kv/VAULT/pin"},
		{http.MethodDelete, "/v1/kv/VAULT/pin"},
		{http.MethodGet, "/v1/kv/VAULT/pin/history"},
	} {
		if code, _ := send(t, app, tc.method, tc.path, "", "application/json", "{}"); code != http.StatusForbidden {
			t.Errorf("no-principal %s %s = %d, want 403", tc.method, tc.path, code)
		}
	}
}

// TestRidesThePlaneFromItsOwnBinary is the property the split introduced.
//
// In production this app is its own process: there is no embedded server in it,
// so pubsub.Bus dials the ONE plane at the address the bus knob names. The
// arrangement below is exactly that — a plane running with no door of its own
// on this app, reached over CLOUD_PUBSUB_URL — and the six ops must be
// indistinguishable from the in-process case. If they are not, the split
// shipped a capability that only works when it is not split.
func TestRidesThePlaneFromItsOwnBinary(t *testing.T) {
	url := plane(t)

	// The door, with nothing but the knob to find the bus by.
	t.Setenv("CLOUD_PUBSUB_URL", url)
	t.Setenv("CLOUD_PUBSUB_PORT", "") // and no server of its own to fall back on
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = pubsub.Shutdown(context.Background()) })

	const org = "org_far"
	if code, raw := doJSON(t, app, http.MethodPost, "/v1/kv/REMOTE", org, nil); code != http.StatusCreated {
		t.Fatalf("create over the wire = %d: %s", code, raw)
	}
	if code, _ := doJSON(t, app, http.MethodPut, "/v1/kv/REMOTE/k", org, map[string]any{"value": "v"}); code != http.StatusOK {
		t.Fatal("put over the wire")
	}
	code, raw := send(t, app, http.MethodGet, "/v1/kv/REMOTE/k", org, "", "")
	if code != http.StatusOK {
		t.Fatalf("get over the wire = %d: %s", code, raw)
	}
	if e := obj(t, raw); e["value"] != "v" {
		t.Errorf("entry = %v, want the value written over the same plane", e)
	}
}

// plane starts the node DIRECTLY, not through pubsub.Mount, and returns the
// address to dial it at.
//
// That distinction is the whole test. Mounting would leave the server parked in
// apps/pubsub where the in-process dial finds it, and the arrangement being
// proved is the one where it is NOT there — the kv binary, whose only way to
// the plane is the address the knob names. Opening the node here is the closest
// a single process gets to the far side of a process boundary.
func plane(t *testing.T) string {
	t.Helper()
	s, err := psembed.Open(psembed.Options{
		Host:       "127.0.0.1",
		Port:       -1, // any free port; ClientURL says which
		ServerName: "kv-test-plane",
		StoreDir:   t.TempDir(),
	})
	if err != nil {
		t.Fatalf("open the plane: %v", err)
	}
	t.Cleanup(s.Shutdown)
	return s.ClientURL()
}

// TestBusUnreachableFailsClosed: with no plane to dial, every op answers 503 —
// never a 200 over a store that is not there, and never a 404 that would read
// as "your bucket is gone".
func TestBusUnreachableFailsClosed(t *testing.T) {
	// A port nothing is listening on: the dial fails rather than hanging.
	t.Setenv("CLOUD_PUBSUB_URL", "nats://127.0.0.1:1")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v — mounting must not depend on the plane being up", err)
	}
	t.Cleanup(func() { _ = pubsub.Shutdown(context.Background()) })

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/kv/B"},
		{http.MethodDelete, "/v1/kv/B"},
		{http.MethodGet, "/v1/kv/B/K"},
		{http.MethodPut, "/v1/kv/B/K"},
		{http.MethodDelete, "/v1/kv/B/K"},
		{http.MethodGet, "/v1/kv/B/K/history"},
	} {
		var body any
		if tc.method == http.MethodPut {
			body = map[string]any{"value": "x"}
		}
		if code, raw := doJSON(t, app, tc.method, tc.path, "org_down", body); code != http.StatusServiceUnavailable {
			t.Errorf("%s %s with no plane = %d, want 503: %s", tc.method, tc.path, code, raw)
		}
	}
}

// TestEveryRouteIsTypedAndDescribed fails when a kv operation is not a typed
// op, or reaches the document with no prose. There is no untyped-by-design list
// here: this surface was BORN typed, so the next route added is typed by
// default or this test names it.
func TestEveryRouteIsTypedAndDescribed(t *testing.T) {
	app := mount(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "kv", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	// The health route is Serve's, not this package's.
	ours := func(p string) bool { return strings.HasPrefix(p, "/v1/kv") && p != "/v1/kv/health" }

	served, typed := map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		if !ours(path) {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && ours(key[i+1:]) {
			typed[key] = op.Description
		}
	}

	if len(served) == 0 {
		t.Fatal("the router serves no kv routes")
	}
	if len(typed) != 6 {
		t.Errorf("typed ops = %d, want the 6 the door declares", len(typed))
	}
	var bad []string
	for key := range served {
		if _, ok := typed[key]; !ok {
			bad = append(bad, key)
		}
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		t.Errorf("operation(s) with no registry entry: %s\n"+
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command "+
			"and no SDK method.", strings.Join(bad, ", "))
	}
	var bare []string
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			bare = append(bare, key)
		}
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("typed op(s) with no description: %s\n"+
			"Run: go generate -run zipdoc ./apps/kv/...", strings.Join(bare, ", "))
	}
}

// TestMessagingIsNotThisApps pins the split from this side: publish and request
// are the bus's, and this binary must not answer them.
func TestMessagingIsNotThisApps(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Use(cloud.Bridge())
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	for _, path := range []string{"/v1/pubsub/publish", "/v1/pubsub/request", "/v1/kv/publish/x/y"} {
		if code, _ := send(t, app, http.MethodPost, path, "org_x", "application/json", "{}"); code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404 — messaging answers under its own name", path, code)
		}
	}
}

// TestMountRefusesARouterWithNoRegistry: a typed op is a route PLUS a registry
// entry, so a router that cannot hold the registry would serve six routes with
// no schema, no prose, no MCP tool and no SDK method. Mount fails instead.
func TestMountRefusesARouterWithNoRegistry(t *testing.T) {
	if err := Mount(bare{}, cloud.Deps{}); err == nil {
		t.Fatal("Mount accepted a router with no registry — the surface would publish nothing")
	}
}

// bare is a router that is not a zip app.
type bare struct{ cloud.Router }
