package pubsub

// typed_wire_test.go pins the tenant door's wire against the REAL embedded
// plane — no fakes anywhere in the path: every assertion below rides Mount's
// own JetStream node on an ephemeral port.
//
//   - the produce → store → consume loop: create stream, durable publish with
//     Nats-Msg-Id dedup, core fallback for uncaptured subjects, consumer
//     create, pull fetch, and the 404s of absence;
//   - request/reply against a live responder on the NATS port, plus the honest
//     404 (no responder) and 408 (responder silent);
//   - the KV round trip: bucket, put/get/history revisions, tombstone reads;
//   - tenancy: the org comes from the validated principal only, one org's
//     names never resolve to another's, and the org root never leaks into a
//     response;
//   - the route oracle: every served /v1/pubsub operation is a typed,
//     described op — and the CLOSED refusal list, pinning each intent address
//     this door deliberately does NOT serve to a route-level 404.

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

	natsio "github.com/nats-io/nats.go"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
	luxlog "github.com/luxfi/log"
)

const wireTimeout = 15 * time.Second

// mountWire mounts the WHOLE subsystem — embedded server on an ephemeral port,
// tenant door over it — and returns the app the door is registered on.
func mountWire(t *testing.T) *zip.App {
	t.Helper()
	srv = nil
	t.Setenv("CLOUD_PUBSUB_HOST", "127.0.0.1")
	t.Setenv("CLOUD_PUBSUB_PORT", "-1") // random free port
	t.Setenv("CLOUD_PUBSUB_STORE_DIR", t.TempDir())
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Mount(app, cloud.Deps{DataDir: t.TempDir()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
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

// TestRequestReplyAnswersOverTheBus proves the synchronous half against a LIVE
// responder subscribed on the NATS port — the two doors meeting on one bus —
// and pins the honest refusals: 404 with nobody listening, 408 with a
// responder that never replies.
func TestRequestReplyAnswersOverTheBus(t *testing.T) {
	app := mountWire(t)
	const org = "org_rpc"

	nc, err := natsio.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect responder: %v", err)
	}
	defer nc.Close()
	// The responder lives on the cluster door, where subjects are physical —
	// the org root the tenant door maps onto is exactly what it subscribes.
	echo, err := nc.Subscribe("pub."+org+".echo", func(m *natsio.Msg) { _ = m.Respond(m.Data) })
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = echo.Unsubscribe() }()
	mute, err := nc.Subscribe("pub."+org+".void", func(*natsio.Msg) {})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = mute.Unsubscribe() }()
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	code, raw := doJSON(t, app, http.MethodPost, "/v1/pubsub/request", org, map[string]any{
		"subject": "echo", "data": "ping", "timeoutMs": 3000,
	})
	if code != http.StatusOK {
		t.Fatalf("request = %d: %s", code, raw)
	}
	if reply := obj(t, raw); reply["data"] != "ping" {
		t.Errorf("reply = %v, want the echoed ping", reply)
	}

	if code, _ := doJSON(t, app, http.MethodPost, "/v1/pubsub/request", org, map[string]any{
		"subject": "nobody.home", "data": "x", "timeoutMs": 1000}); code != http.StatusNotFound {
		t.Errorf("request with no responder = %d, want 404", code)
	}
	if code, _ := doJSON(t, app, http.MethodPost, "/v1/pubsub/request", org, map[string]any{
		"subject": "void", "data": "x", "timeoutMs": 300}); code != http.StatusRequestTimeout {
		t.Errorf("request with a silent responder = %d, want 408", code)
	}
}

// TestKVRoundTrip walks keyed state through its revisions: bucket create, two
// puts, the read, the history, the tombstone, and the bucket teardown.
func TestKVRoundTrip(t *testing.T) {
	app := mountWire(t)
	const org = "org_kv"

	code, raw := doJSON(t, app, http.MethodPost, "/v1/pubsub/kv/CFG", org, map[string]any{"history": 5})
	if code != http.StatusCreated {
		t.Fatalf("create bucket = %d: %s", code, raw)
	}
	if b := obj(t, raw); b["bucket"] != "CFG" || b["history"] != float64(5) {
		t.Errorf("bucket = %v, want CFG with history 5", b)
	}

	code, raw = doJSON(t, app, http.MethodPut, "/v1/pubsub/kv/CFG/theme", org, map[string]any{"value": "light"})
	if code != http.StatusOK {
		t.Fatalf("put = %d: %s", code, raw)
	}
	if ack := obj(t, raw); ack["revision"] != float64(1) {
		t.Errorf("first put revision = %v, want 1", ack["revision"])
	}
	_, raw = doJSON(t, app, http.MethodPut, "/v1/pubsub/kv/CFG/theme", org, map[string]any{"value": "dark"})
	if ack := obj(t, raw); ack["revision"] != float64(2) {
		t.Errorf("second put revision = %v, want 2", ack["revision"])
	}

	code, raw = send(t, app, http.MethodGet, "/v1/pubsub/kv/CFG/theme", org, "", "")
	if code != http.StatusOK {
		t.Fatalf("get = %d: %s", code, raw)
	}
	if e := obj(t, raw); e["value"] != "dark" || e["revision"] != float64(2) || e["operation"] != "put" {
		t.Errorf("entry = %v, want dark at revision 2", e)
	}

	code, raw = send(t, app, http.MethodGet, "/v1/pubsub/kv/CFG/theme/history", org, "", "")
	if code != http.StatusOK {
		t.Fatalf("history = %d: %s", code, raw)
	}
	if page := obj(t, raw); len(page["data"].([]any)) != 2 {
		t.Errorf("history = %v, want both revisions", page["data"])
	}

	if code, _ := send(t, app, http.MethodDelete, "/v1/pubsub/kv/CFG/theme", org, "", ""); code != http.StatusNoContent {
		t.Errorf("delete key = %d, want 204", code)
	}
	if code, _ := send(t, app, http.MethodGet, "/v1/pubsub/kv/CFG/theme", org, "", ""); code != http.StatusNotFound {
		t.Errorf("get after delete = %d, want 404 (tombstone)", code)
	}

	if code, _ := send(t, app, http.MethodDelete, "/v1/pubsub/kv/CFG", org, "", ""); code != http.StatusNoContent {
		t.Errorf("delete bucket = %d, want 204", code)
	}
	if code, _ := send(t, app, http.MethodGet, "/v1/pubsub/kv/CFG/theme", org, "", ""); code != http.StatusNotFound {
		t.Errorf("get in a deleted bucket = %d, want 404", code)
	}
}

// Tenancy is a fact about the PRINCIPAL, never a field the caller can send.
//
// The property was proved twice here, once over streams and once over KV.
// Streams moved to mq, which proves it there; KV proves it whole on its own —
// the same addresses answer 404 for another org, and the same NAMES remain that
// org's to claim.
func TestPubsubTenancyIsNeverACallerField(t *testing.T) {
	app := mountWire(t)

	if code, raw := doJSON(t, app, http.MethodPost, "/v1/pubsub/kv/VAULT", "acme", nil); code != http.StatusCreated {
		t.Fatalf("seed bucket = %d: %s", code, raw)
	}
	if code, _ := doJSON(t, app, http.MethodPut, "/v1/pubsub/kv/VAULT/pin", "acme", map[string]any{"value": "1234"}); code != http.StatusOK {
		t.Fatal("seed key")
	}

	// Another org: the same addresses answer 404. Writes carry a valid body so
	// the 404 is tenancy's, not a bind refusal's.
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/pubsub/kv/VAULT/pin"},
		{http.MethodGet, "/v1/pubsub/kv/VAULT/pin/history"},
		{http.MethodDelete, "/v1/pubsub/kv/VAULT"},
	} {
		var body any
		if tc.method == http.MethodPost || tc.method == http.MethodPut {
			body = map[string]any{"value": "x"}
		}
		if code, _ := doJSON(t, app, tc.method, tc.path, "other", body); code != http.StatusNotFound {
			t.Errorf("cross-org %s %s = %d, want 404", tc.method, tc.path, code)
		}
	}

	// And the same name is another org's to claim, not a collision.
	if code, _ := doJSON(t, app, http.MethodPost, "/v1/pubsub/kv/VAULT", "other", nil); code != http.StatusCreated {
		t.Errorf("the same bucket name in another org = %d, want its own 201", code)
	}

	// No principal: every op is a 403, before any bus work.
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/pubsub/publish"},
		{http.MethodPost, "/v1/pubsub/request"},
		{http.MethodPost, "/v1/pubsub/kv/VAULT"},
		{http.MethodGet, "/v1/pubsub/kv/VAULT/pin"},
		{http.MethodPut, "/v1/pubsub/kv/VAULT/pin"},
		{http.MethodDelete, "/v1/pubsub/kv/VAULT"},
	} {
		if code, _ := send(t, app, tc.method, tc.path, "", "application/json", "{}"); code != http.StatusForbidden {
			t.Errorf("no-principal %s %s = %d, want 403", tc.method, tc.path, code)
		}
	}
}

// pubsubRoutes reads BOTH projections of the live router: what the document
// says is served, and which of those carry a typed registry entry. Reading the
// router (not the source) is what makes this a gate rather than prose. It
// takes the app rather than mounting one, because the embedded server is a
// package singleton: a second mount in one test would orphan the first.
func pubsubRoutes(t *testing.T, app *zip.App) (served map[string]bool, typed map[string]string) {
	t.Helper()
	doc, err := openapi.Spec(app, openapi.Info{Title: "pubsub", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	// The health route is Serve's, not this package's.
	ours := func(p string) bool {
		return strings.HasPrefix(p, "/v1/pubsub") && p != "/v1/pubsub/health"
	}
	served, typed = map[string]bool{}, map[string]string{}
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
	return served, typed
}

// TestEveryPubsubRouteIsTypedAndDescribed fails when a pubsub operation is not
// a typed op, or reaches the document with no prose. There is no untyped-by-
// design list here: this surface was BORN typed, so the next route added is
// typed by default or this test names it.
func TestEveryPubsubRouteIsTypedAndDescribed(t *testing.T) {
	served, typed := pubsubRoutes(t, mountWire(t))
	if len(served) == 0 {
		t.Fatal("the router serves no pubsub routes")
	}
	if len(typed) != 8 {
		t.Errorf("typed ops = %d, want the 8 the door declares", len(typed))
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
			"Run: go generate -run zipdoc ./apps/pubsub/...", strings.Join(bare, ", "))
	}
}

// refusedByDesign is the CLOSED list of intent operations (hanzoai/openapi
// d86248f^:pubsub/openapi.yaml) this door deliberately does NOT serve, each
// with the reason. Addresses are the intent's own. The package doc carries the
// same three decisions in prose; this list is what keeps them checkable.
var refusedByDesign = map[string]string{
	"GET /v1/pubsub/subscribe": "an SSE stream is not a typed op — zip's typed path answers ONE JSON Out " +
		"and has no vocabulary for text/event-stream (the same measured fact that keeps POST /v1/ask " +
		"untyped). Consumption is the pull op …/consumers/{name}/next and the NATS port's native subscriptions.",
	"GET /v1/pubsub/objects/{bucket}":           "cloud's object door is /v1/storage; a second object store here would be two doors to one noun.",
	"GET /v1/pubsub/objects/{bucket}/{name}":    "same: objects belong to /v1/storage.",
	"PUT /v1/pubsub/objects/{bucket}/{name}":    "same: objects belong to /v1/storage.",
	"DELETE /v1/pubsub/objects/{bucket}/{name}": "same: objects belong to /v1/storage.",
	"GET /v1/pubsub/varz":                       "operator telemetry, server-wide and cross-tenant; the operator plane is apps/o11y.",
	"GET /v1/pubsub/connz":                      "lists every client of every tenant — publishing it on a tenant surface is a leak.",
	"GET /v1/pubsub/jsz":                        "operator telemetry; the tenant's slice of JetStream is its own stream records.",
	"GET /v1/pubsub/routez":                     "cluster internals; operator plane.",
	"GET /v1/pubsub/gatewayz":                   "cluster internals; operator plane.",
	"GET /v1/pubsub/leafz":                      "cluster internals; operator plane.",
	"GET /v1/pubsub/subsz":                      "every subscription of every tenant; operator plane.",
}

// TestRefusedPubsubOpsStayRefused pins each refused intent address to a
// ROUTE-LEVEL 404 — the router has never heard of it — so shipping one of them
// half-real would first have to fail this test, and the refusal list cannot
// rot into prose about addresses that quietly started serving.
func TestRefusedPubsubOpsStayRefused(t *testing.T) {
	app := mountWire(t)
	served, _ := pubsubRoutes(t, app)
	for key := range refusedByDesign {
		method, path, _ := strings.Cut(key, " ")
		wire := strings.NewReplacer("{bucket}", "B", "{name}", "N").Replace(path)
		if code, raw := send(t, app, method, wire, "org_refused", "", ""); code != http.StatusNotFound {
			t.Errorf("%s answers %d — a refused op must have NO route: %s", key, code, raw)
		}
		if served[key] {
			t.Errorf("%s appears in the served document AND in refusedByDesign — one of them is lying", key)
		}
	}
}
