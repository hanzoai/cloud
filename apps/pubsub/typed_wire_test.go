package pubsub

// typed_wire_test.go pins the tenant endpoint's wire against the REAL embedded
// plane — no fakes anywhere in the path: every assertion below rides Mount's
// own JetStream node on an ephemeral port.
//
//   - request/reply against a live responder on the NATS port, plus the honest
//     404 (no responder) and 408 (responder silent);
//   - publish: the core path, and the durable path through a stream that
//     captures the subject, named the way the caller names it;
//   - tenancy: the org comes from the validated principal only, one org's
//     subjects are not another's to name, and the org root never leaks into a
//     response;
//   - the route oracle: every served /v1/pubsub operation is a typed,
//     described op — and the CLOSED refusal list, pinning each intent address
//     this surface deliberately does NOT serve to a route-level 404.
//
// Key-value is apps/kv's, and is proved there against this same plane.

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
// tenant endpoint over it — and returns the app the endpoint is registered on.
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
// responder subscribed on the NATS port — the two entry points meeting on one
// bus — and pins the honest refusals: 404 with nobody listening, 408 with a
// responder that never replies.
func TestRequestReplyAnswersOverTheBus(t *testing.T) {
	app := mountWire(t)
	const org = "org_rpc"

	nc, err := natsio.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect responder: %v", err)
	}
	defer nc.Close()
	// The responder lives on the cluster listener, where subjects are physical —
	// the org root the tenant endpoint maps onto is exactly what it subscribes.
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
	reply := obj(t, raw)
	if reply["data"] != "ping" {
		t.Errorf("reply = %v, want the echoed ping", reply)
	}
	// The caller's own namespace comes back, never the plane's.
	if subj, _ := reply["subject"].(string); strings.Contains(subj, "pub."+org) {
		t.Errorf("reply subject = %q — the org root leaked into the response", subj)
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

// TestPublishTakesBothPaths walks one message out core — nothing captures the
// subject, so the receipt is a bare ok — and one into a stream that does, where
// the receipt names the stream the way the CALLER names it rather than by its
// plane-wide name.
func TestPublishTakesBothPaths(t *testing.T) {
	app := mountWire(t)
	const org = "org_pub"

	code, raw := doJSON(t, app, http.MethodPost, "/v1/pubsub/publish", org, map[string]any{
		"subject": "nothing.captures.this", "data": "loose",
	})
	if code != http.StatusOK {
		t.Fatalf("core publish = %d: %s", code, raw)
	}
	if ack := obj(t, raw); ack["ok"] != true || ack["stream"] != nil {
		t.Errorf("core ack = %v, want {ok} with no stream — nothing retained it", ack)
	}

	// A stream on the plane, created where streams are created: the cluster
	// listener. Its physical name is the org's, so the tenant endpoint's receipt
	// has a prefix to strip.
	nc, err := natsio.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	if _, err := js.AddStream(&natsio.StreamConfig{
		Name:     TenantPrefix + org + "-ORDERS",
		Subjects: []string{"pub." + org + ".orders.>"},
	}); err != nil {
		t.Fatalf("add stream: %v", err)
	}

	code, raw = doJSON(t, app, http.MethodPost, "/v1/pubsub/publish", org, map[string]any{
		"subject": "orders.created", "data": `{"id":"o_1"}`,
		"headers": map[string]string{"Nats-Msg-Id": "o_1"},
	})
	if code != http.StatusOK {
		t.Fatalf("durable publish = %d: %s", code, raw)
	}
	ack := obj(t, raw)
	if ack["stream"] != "ORDERS" {
		t.Errorf("ack stream = %v, want ORDERS — the caller's name, not the plane's", ack["stream"])
	}
	if ack["seq"] != float64(1) {
		t.Errorf("ack seq = %v, want 1", ack["seq"])
	}

	// The same Nats-Msg-Id inside the dedup window is acknowledged as a
	// duplicate rather than stored again.
	_, raw = doJSON(t, app, http.MethodPost, "/v1/pubsub/publish", org, map[string]any{
		"subject": "orders.created", "data": `{"id":"o_1"}`,
		"headers": map[string]string{"Nats-Msg-Id": "o_1"},
	})
	if dup := obj(t, raw); dup["duplicate"] != true {
		t.Errorf("second publish of the same Nats-Msg-Id = %v, want duplicate", dup)
	}
}

// Tenancy is a fact about the PRINCIPAL, never a field the caller can send.
//
// One org's subjects are not another's to name: the endpoint roots every subject
// in the caller's own namespace, so a responder living in one org's root is
// simply not there for another — the same address, a different plane.
func TestPubsubTenancyIsNeverACallerField(t *testing.T) {
	app := mountWire(t)

	nc, err := natsio.Connect(srv.ClientURL())
	if err != nil {
		t.Fatalf("connect responder: %v", err)
	}
	defer nc.Close()
	echo, err := nc.Subscribe("pub.acme.echo", func(m *natsio.Msg) { _ = m.Respond([]byte("acme")) })
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer func() { _ = echo.Unsubscribe() }()
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if code, raw := doJSON(t, app, http.MethodPost, "/v1/pubsub/request", "acme", map[string]any{
		"subject": "echo", "data": "x", "timeoutMs": 3000}); code != http.StatusOK {
		t.Fatalf("acme reaching its own responder = %d: %s", code, raw)
	}
	if code, _ := doJSON(t, app, http.MethodPost, "/v1/pubsub/request", "other", map[string]any{
		"subject": "echo", "data": "x", "timeoutMs": 1000}); code != http.StatusNotFound {
		t.Errorf("another org reaching it = %d, want 404 — the subject is not theirs to name", code)
	}

	// A caller cannot climb out of its root, and cannot name a set.
	for _, subject := range []string{"..", ">", "*", "orders.>"} {
		if code, _ := doJSON(t, app, http.MethodPost, "/v1/pubsub/publish", "acme", map[string]any{
			"subject": subject, "data": "x"}); code != http.StatusBadRequest {
			t.Errorf("publish to %q = %d, want 400", subject, code)
		}
	}

	// No principal: every op is a 403, before any bus work.
	for _, path := range []string{"/v1/pubsub/publish", "/v1/pubsub/request"} {
		if code, _ := send(t, app, http.MethodPost, path, "", "application/json", "{}"); code != http.StatusForbidden {
			t.Errorf("no-principal POST %s = %d, want 403", path, code)
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
	// ONE prefix, because this app answers at one address. The health route is
	// Serve's, not this package's.
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
	if len(typed) != 2 {
		t.Errorf("typed ops = %d, want the 2 the surface declares (publish, request)", len(typed))
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

// TestKeyValueIsNotThisApps pins the split from this side: /v1/kv is another
// capability's address and this binary must not answer it. A route that grew
// back here would otherwise be invisible — the ratchet only measures the composed
// document, and a duplicate mount reads there as one operation.
func TestKeyValueIsNotThisApps(t *testing.T) {
	app := mountWire(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/kv/B"},
		{http.MethodGet, "/v1/kv/B/K"},
		{http.MethodPut, "/v1/kv/B/K"},
		{http.MethodDelete, "/v1/kv/B/K"},
		{http.MethodGet, "/v1/kv/B/K/history"},
		{http.MethodPost, "/v1/pubsub/kv/B"},
	} {
		if code, raw := send(t, app, tc.method, tc.path, "org_kv", "", ""); code != http.StatusNotFound {
			t.Errorf("%s %s = %d, want 404 — key-value answers under its own name: %s", tc.method, tc.path, code, raw)
		}
	}
}

// refusedByDesign is the CLOSED list of intent operations (hanzoai/openapi
// d86248f^:pubsub/openapi.yaml) this surface deliberately does NOT serve, each
// with the reason. Addresses are the intent's own. The package doc carries the
// same three decisions in prose; this list is what keeps them checkable.
var refusedByDesign = map[string]string{
	"GET /v1/pubsub/subscribe": "an SSE stream is not a typed op — zip's typed path answers ONE JSON Out " +
		"and has no vocabulary for text/event-stream (the same measured fact that keeps POST /v1/ask " +
		"untyped). Consumption is the NATS port's native subscriptions and apps/mq's pull ops.",
	"GET /v1/pubsub/objects/{bucket}":           "cloud's object endpoint is /v1/storage; a second object store here would be two endpoints to one noun.",
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
