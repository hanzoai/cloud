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
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}); err != nil {
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

// TestTypedOpsDriveTheEmbeddedBus walks the produce→store→consume loop through
// the door and asserts at each step that what came back is the plane's own
// answer — sequences, dedup, pending counts — in the org's OWN view, with the
// physical namespace never leaking.
func TestTypedOpsDriveTheEmbeddedBus(t *testing.T) {
	app := mountWire(t)
	const org = "org_wire"

	code, raw := doJSON(t, app, http.MethodPost, "/v1/pubsub/jetstream/streams", org, map[string]any{
		"name": "ORDERS", "subjects": []string{"orders.>"},
	})
	if code != http.StatusCreated {
		t.Fatalf("create stream = %d, want 201: %s", code, raw)
	}
	st := obj(t, raw)
	if st["name"] != "ORDERS" {
		t.Errorf("stream name = %v, want ORDERS", st["name"])
	}
	if subs, _ := st["subjects"].([]any); len(subs) != 1 || subs[0] != "orders.>" {
		t.Errorf("subjects = %v, want the caller's own view [orders.>]", st["subjects"])
	}
	if s := string(raw); strings.Contains(s, "t-"+org) || strings.Contains(s, "pub."+org) {
		t.Errorf("physical namespace leaked into the record: %s", s)
	}

	t.Run("publish is durable when captured, dedups by Nats-Msg-Id, else goes core", func(t *testing.T) {
		code, raw := doJSON(t, app, http.MethodPost, "/v1/pubsub/publish", org, map[string]any{
			"subject": "orders.created", "data": `{"id":"o_1"}`,
		})
		if code != http.StatusOK {
			t.Fatalf("publish = %d: %s", code, raw)
		}
		ack := obj(t, raw)
		if ack["ok"] != true || ack["stream"] != "ORDERS" || ack["seq"] != float64(1) {
			t.Errorf("ack = %v, want ok on ORDERS seq 1", ack)
		}

		dedup := map[string]any{"subject": "orders.created", "data": `{"id":"o_2"}`,
			"headers": map[string]string{"Nats-Msg-Id": "o_2"}}
		_, raw = doJSON(t, app, http.MethodPost, "/v1/pubsub/publish", org, dedup)
		if ack := obj(t, raw); ack["duplicate"] == true {
			t.Fatalf("first o_2 marked duplicate: %v", ack)
		}
		_, raw = doJSON(t, app, http.MethodPost, "/v1/pubsub/publish", org, dedup)
		if ack := obj(t, raw); ack["duplicate"] != true {
			t.Errorf("repeated Nats-Msg-Id not deduplicated: %v", ack)
		}

		// Nothing captures audit.>: the message goes out core and the receipt
		// honestly names no stream.
		code, raw = doJSON(t, app, http.MethodPost, "/v1/pubsub/publish", org, map[string]any{
			"subject": "audit.login", "data": "x",
		})
		if code != http.StatusOK {
			t.Fatalf("core publish = %d: %s", code, raw)
		}
		if ack := obj(t, raw); ack["ok"] != true || ack["stream"] != nil {
			t.Errorf("core ack = %v, want ok with no stream", ack)
		}

		// A wildcard is not a publish target.
		if code, _ := doJSON(t, app, http.MethodPost, "/v1/pubsub/publish", org, map[string]any{
			"subject": "orders.*", "data": "x"}); code != http.StatusBadRequest {
			t.Errorf("publish to wildcard = %d, want 400", code)
		}
	})

	t.Run("a consumer is a cursor and fetch drains it", func(t *testing.T) {
		code, raw := doJSON(t, app, http.MethodPost, "/v1/pubsub/jetstream/streams/ORDERS/consumers", org,
			map[string]any{"name": "worker", "filter": "orders.created"})
		if code != http.StatusCreated {
			t.Fatalf("create consumer = %d: %s", code, raw)
		}
		c := obj(t, raw)
		if c["name"] != "worker" || c["stream"] != "ORDERS" || c["filter"] != "orders.created" {
			t.Errorf("consumer record = %v, want worker on ORDERS filtered to the org's own view", c)
		}
		if c["pending"] != float64(2) {
			t.Errorf("pending = %v, want the 2 stored messages", c["pending"])
		}

		code, raw = doJSON(t, app, http.MethodPost, "/v1/pubsub/jetstream/streams/ORDERS/consumers/worker/next", org,
			map[string]any{"batch": 10, "waitMs": 300})
		if code != http.StatusOK {
			t.Fatalf("next = %d: %s", code, raw)
		}
		page := obj(t, raw)
		msgs, _ := page["data"].([]any)
		if len(msgs) != 2 {
			t.Fatalf("fetched %d messages, want 2: %s", len(msgs), raw)
		}
		first := msgs[0].(map[string]any)
		if first["subject"] != "orders.created" || first["data"] != `{"id":"o_1"}` || first["seq"] != float64(1) {
			t.Errorf("first fetched = %v, want o_1 at seq 1 in the org's own subject view", first)
		}

		// The batch was acked on delivery: a second fetch finds nothing, as an
		// empty page rather than an error.
		_, raw = doJSON(t, app, http.MethodPost, "/v1/pubsub/jetstream/streams/ORDERS/consumers/worker/next", org,
			map[string]any{"waitMs": 200})
		if page := obj(t, raw); len(page["data"].([]any)) != 0 {
			t.Errorf("second fetch = %v, want empty (at-most-once hand-off)", page["data"])
		}

		code, raw = send(t, app, http.MethodGet, "/v1/pubsub/jetstream/streams/ORDERS/consumers", org, "", "")
		if code != http.StatusOK {
			t.Fatalf("list consumers = %d: %s", code, raw)
		}
		if page := obj(t, raw); len(page["data"].([]any)) != 1 {
			t.Errorf("consumers = %v, want one", page["data"])
		}

		if code, _ := send(t, app, http.MethodDelete, "/v1/pubsub/jetstream/streams/ORDERS/consumers/worker", org, "", ""); code != http.StatusNoContent {
			t.Errorf("delete consumer = %d, want 204", code)
		}
		if code, _ := send(t, app, http.MethodDelete, "/v1/pubsub/jetstream/streams/ORDERS/consumers/worker", org, "", ""); code != http.StatusNotFound {
			t.Errorf("second delete = %d, want 404", code)
		}
	})

	t.Run("stream reads, update and delete carry the plane's own state", func(t *testing.T) {
		code, raw := send(t, app, http.MethodGet, "/v1/pubsub/jetstream/streams/ORDERS", org, "", "")
		if code != http.StatusOK {
			t.Fatalf("get stream = %d: %s", code, raw)
		}
		if st := obj(t, raw); st["messages"] != float64(2) {
			t.Errorf("messages = %v, want the 2 stored", st["messages"])
		}

		code, raw = send(t, app, http.MethodGet, "/v1/pubsub/jetstream/streams", org, "", "")
		if code != http.StatusOK {
			t.Fatalf("list = %d: %s", code, raw)
		}
		if page := obj(t, raw); len(page["data"].([]any)) != 1 {
			t.Errorf("streams = %v, want one", page["data"])
		}

		code, raw = doJSON(t, app, http.MethodPut, "/v1/pubsub/jetstream/streams/ORDERS", org, map[string]any{
			"subjects": []string{"orders.>", "refunds.>"}, "maxMsgs": 1000,
		})
		if code != http.StatusOK {
			t.Fatalf("update = %d: %s", code, raw)
		}
		up := obj(t, raw)
		if subs, _ := up["subjects"].([]any); len(subs) != 2 {
			t.Errorf("updated subjects = %v, want two", up["subjects"])
		}
		if up["maxMsgs"] != float64(1000) || up["storage"] != "file" {
			t.Errorf("updated = %v, want maxMsgs 1000 with storage kept", up)
		}

		if code, _ := send(t, app, http.MethodDelete, "/v1/pubsub/jetstream/streams/ORDERS", org, "", ""); code != http.StatusNoContent {
			t.Errorf("delete stream = %d, want 204", code)
		}
		if code, _ := send(t, app, http.MethodGet, "/v1/pubsub/jetstream/streams/ORDERS", org, "", ""); code != http.StatusNotFound {
			t.Errorf("get after delete = %d, want 404", code)
		}
	})
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

// TestPubsubTenancyIsNeverACallerField pins the one rule a typed op can
// silently break: the org comes from the VALIDATED principal cloud.Bridge
// parks, never from an In field. One org's names never resolve to another's
// resources; the same name held by two orgs is two disjoint resources; and an
// unvalidated caller is refused on every op.
func TestPubsubTenancyIsNeverACallerField(t *testing.T) {
	app := mountWire(t)

	code, raw := doJSON(t, app, http.MethodPost, "/v1/pubsub/jetstream/streams", "acme", map[string]any{
		"name": "SECRET", "subjects": []string{"deals.>"},
	})
	if code != http.StatusCreated {
		t.Fatalf("seed stream = %d: %s", code, raw)
	}
	if code, raw := doJSON(t, app, http.MethodPost, "/v1/pubsub/kv/VAULT", "acme", nil); code != http.StatusCreated {
		t.Fatalf("seed bucket = %d: %s", code, raw)
	}
	if code, _ := doJSON(t, app, http.MethodPut, "/v1/pubsub/kv/VAULT/pin", "acme", map[string]any{"value": "1234"}); code != http.StatusOK {
		t.Fatal("seed key")
	}

	// Another org: the same addresses answer 404, and the same NAMES are its
	// own disjoint namespace to claim. Writes carry a valid body so the 404 is
	// tenancy's, not a bind refusal's.
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/pubsub/jetstream/streams/SECRET"},
		{http.MethodPut, "/v1/pubsub/jetstream/streams/SECRET"},
		{http.MethodDelete, "/v1/pubsub/jetstream/streams/SECRET"},
		{http.MethodGet, "/v1/pubsub/jetstream/streams/SECRET/consumers"},
		{http.MethodPost, "/v1/pubsub/jetstream/streams/SECRET/consumers"},
		{http.MethodGet, "/v1/pubsub/kv/VAULT/pin"},
		{http.MethodGet, "/v1/pubsub/kv/VAULT/pin/history"},
		{http.MethodDelete, "/v1/pubsub/kv/VAULT"},
	} {
		var body any
		if tc.method == http.MethodPost || tc.method == http.MethodPut {
			body = map[string]any{"name": "x", "subjects": []string{"x.>"}, "value": "x"}
		}
		if code, _ := doJSON(t, app, tc.method, tc.path, "other", body); code != http.StatusNotFound {
			t.Errorf("cross-org %s %s = %d, want 404", tc.method, tc.path, code)
		}
	}
	code, raw = send(t, app, http.MethodGet, "/v1/pubsub/jetstream/streams", "other", "", "")
	if code != http.StatusOK {
		t.Fatalf("cross-org list = %d", code)
	}
	if page := obj(t, raw); len(page["data"].([]any)) != 0 {
		t.Errorf("another org's listing = %v, want empty", page["data"])
	}
	if code, _ := doJSON(t, app, http.MethodPost, "/v1/pubsub/jetstream/streams", "other", map[string]any{
		"name": "SECRET", "subjects": []string{"deals.>"}}); code != http.StatusCreated {
		t.Errorf("the same name in another org = %d, want its own 201", code)
	}

	// acme's messages land in acme's stream only, even under identical
	// logical subjects.
	if code, _ := doJSON(t, app, http.MethodPost, "/v1/pubsub/publish", "acme", map[string]any{
		"subject": "deals.won", "data": "x"}); code != http.StatusOK {
		t.Fatal("acme publish")
	}
	_, raw = send(t, app, http.MethodGet, "/v1/pubsub/jetstream/streams/SECRET", "other", "", "")
	if st := obj(t, raw); st["messages"] != float64(0) {
		t.Errorf("other org's SECRET holds %v messages, want 0", st["messages"])
	}

	// No principal: every op is a 403, before any bus work.
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/pubsub/publish"},
		{http.MethodPost, "/v1/pubsub/request"},
		{http.MethodGet, "/v1/pubsub/jetstream/streams"},
		{http.MethodPost, "/v1/pubsub/jetstream/streams"},
		{http.MethodGet, "/v1/pubsub/jetstream/streams/SECRET"},
		{http.MethodPut, "/v1/pubsub/jetstream/streams/SECRET"},
		{http.MethodDelete, "/v1/pubsub/jetstream/streams/SECRET"},
		{http.MethodGet, "/v1/pubsub/jetstream/streams/SECRET/consumers"},
		{http.MethodPost, "/v1/pubsub/jetstream/streams/SECRET/consumers"},
		{http.MethodGet, "/v1/pubsub/jetstream/streams/SECRET/consumers/w"},
		{http.MethodDelete, "/v1/pubsub/jetstream/streams/SECRET/consumers/w"},
		{http.MethodPost, "/v1/pubsub/jetstream/streams/SECRET/consumers/w/next"},
		{http.MethodPost, "/v1/pubsub/kv/VAULT"},
		{http.MethodDelete, "/v1/pubsub/kv/VAULT"},
		{http.MethodGet, "/v1/pubsub/kv/VAULT/pin"},
		{http.MethodPut, "/v1/pubsub/kv/VAULT/pin"},
		{http.MethodDelete, "/v1/pubsub/kv/VAULT/pin"},
		{http.MethodGet, "/v1/pubsub/kv/VAULT/pin/history"},
	} {
		if code, _ := send(t, app, tc.method, tc.path, "", "", ""); code != http.StatusForbidden {
			t.Errorf("%s %s with no principal = %d, want 403", tc.method, tc.path, code)
		}
	}

	// Subject escapes are refused before they reach the bus.
	for _, subject := range []string{"", "..", "a b", "a.", ".a"} {
		if code, _ := doJSON(t, app, http.MethodPost, "/v1/pubsub/publish", "acme", map[string]any{
			"subject": subject, "data": "x"}); code != http.StatusBadRequest {
			t.Errorf("publish subject %q = %d, want 400", subject, code)
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
	if len(typed) != 18 {
		t.Errorf("typed ops = %d, want the 18 the door declares", len(typed))
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
