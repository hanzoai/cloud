package mq

// The product against a REAL broker: every test here mounts the surface over
// an embedded Hanzo PubSub node (the same github.com/hanzoai/pubsub/embed the
// pubsub app serves in production) and drives it over HTTP. Nothing is
// stubbed — streams persist, consumers track delivery, tenancy is proven by a
// second org looking and seeing nothing.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	natsio "github.com/nats-io/nats.go"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	psembed "github.com/hanzoai/pubsub/embed"
)

var httpCfg = zip.TestConfig{Timeout: 45 * time.Second, FailOnTimeout: true}

// compose installs what a HOST installs. A subsystem never installs cloud.Bridge
// (Mount says why): the program's composer installs it once at the root, after
// the identity check that mints the validated org and before any subsystem
// registers a route. In production that composer is serve.go. In a test the test
// IS the composer, so it owes the same thing — and a test that skips it does not
// test a stricter program, it tests a program where every org-scoped op answers
// 403 for a reason that would never exist in production.
func compose(app *zip.App) { app.Use(cloud.Bridge()) }

// plane opens a real embedded broker on a random port and mounts the surface
// over it, waiting for the client to connect so the first op is never a 503.
func plane(t *testing.T) *zip.App {
	t.Helper()
	srv, err := psembed.Open(psembed.Options{Host: "127.0.0.1", Port: -1, StoreDir: t.TempDir(), ServerName: "mq-test"})
	if err != nil {
		t.Fatalf("embedded broker: %v", err)
	}
	t.Cleanup(srv.Shutdown)
	t.Setenv("CLOUD_PUBSUB_URL", srv.ClientURL())

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Mount(app, cloud.Deps{}); err != nil {
		t.Fatalf("mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(nil) })
	deadline := time.Now().Add(10 * time.Second)
	for b.nc.Status() != natsio.CONNECTED {
		if time.Now().After(deadline) {
			t.Fatal("client never connected to the embedded broker")
		}
		time.Sleep(20 * time.Millisecond)
	}
	return app
}

// do drives one request. A non-empty org sets BOTH X-Org-Id and X-User-Id,
// the pair the identity boundary mints for a validated principal.
func do(t *testing.T, app *zip.App, method, path, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		r = bytes.NewReader(raw)
	}
	rq := httptest.NewRequest(method, path, r)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u_"+org)
	}
	resp, err := app.Test(rq, httpCfg)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// feed publishes payloads onto the org's subject space through an independent
// NATS client — the broker's own wire, not this surface — and waits for the
// stream to store each one.
func feed(t *testing.T, org, rel string, payloads ...string) {
	t.Helper()
	nc, err := natsio.Connect(b.nc.ConnectedUrl())
	if err != nil {
		t.Fatalf("independent client: %v", err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	for _, p := range payloads {
		if _, err := js.Publish(subjectRoot(org)+rel, []byte(p)); err != nil {
			t.Fatalf("publish %q: %v", p, err)
		}
	}
}

func TestStreamLifecycle(t *testing.T) {
	app := plane(t)
	const org = "acme"

	code, body := do(t, app, http.MethodPost, "/v1/mq/stream", org,
		map[string]any{"name": "orders", "subjects": []string{"orders.>"}, "max_msgs": 100})
	if code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d (%s)", code, body)
	}
	var st Stream
	if err := json.Unmarshal(body, &st); err != nil {
		t.Fatalf("create body: %v", err)
	}
	if st.Name != "orders" || len(st.Config.Subjects) != 1 || st.Config.Subjects[0] != "orders.>" {
		t.Fatalf("created stream presents wrong: %+v", st)
	}

	// The broker really holds it — namespaced, subjects confined to the org's
	// space — proven through an independent client, not this surface.
	nc, err := natsio.Connect(b.nc.ConnectedUrl())
	if err != nil {
		t.Fatalf("independent client: %v", err)
	}
	defer nc.Close()
	js, _ := nc.JetStream()
	info, err := js.StreamInfo("MQ_acme_orders")
	if err != nil {
		t.Fatalf("stream not on the broker under the org namespace: %v", err)
	}
	if got := info.Config.Subjects[0]; got != "mq.acme.orders.>" {
		t.Fatalf("subjects not confined to the org space: %q", got)
	}

	feed(t, org, "orders.new", "one", "two", "three")

	code, body = do(t, app, http.MethodGet, "/v1/mq/stream/orders", org, nil)
	if code != http.StatusOK {
		t.Fatalf("get: want 200, got %d (%s)", code, body)
	}
	_ = json.Unmarshal(body, &st)
	if st.State.Messages != 3 {
		t.Fatalf("stored messages: want 3, got %d", st.State.Messages)
	}

	code, body = do(t, app, http.MethodGet, "/v1/mq/stream/orders/message?seq=2", org, nil)
	if code != http.StatusOK {
		t.Fatalf("read by seq: want 200, got %d (%s)", code, body)
	}
	var read readOut
	_ = json.Unmarshal(body, &read)
	if len(read.Messages) != 1 || read.Messages[0].Subject != "orders.new" {
		t.Fatalf("read by seq: %+v", read)
	}
	if data, _ := base64.StdEncoding.DecodeString(read.Messages[0].Data); string(data) != "two" {
		t.Fatalf("payload: want two, got %q", read.Messages[0].Data)
	}

	code, body = do(t, app, http.MethodGet, "/v1/mq/stream/orders/message?last_by_subject=orders.new", org, nil)
	if code != http.StatusOK {
		t.Fatalf("last_by_subject: want 200, got %d (%s)", code, body)
	}
	_ = json.Unmarshal(body, &read)
	if data, _ := base64.StdEncoding.DecodeString(read.Messages[0].Data); string(data) != "three" {
		t.Fatalf("last: want three, got %q", data)
	}

	code, body = do(t, app, http.MethodGet, "/v1/mq/stream/orders/message?next_by_subject=orders.*&seq=1", org, nil)
	_ = json.Unmarshal(body, &read)
	if code != http.StatusOK || len(read.Messages) != 3 {
		t.Fatalf("walk: want 3 messages, got %d (code %d)", len(read.Messages), code)
	}

	if code, body = do(t, app, http.MethodDelete, "/v1/mq/stream/orders/message/1", org, nil); code != http.StatusNoContent {
		t.Fatalf("delete message: want 204, got %d (%s)", code, body)
	}

	code, body = do(t, app, http.MethodPut, "/v1/mq/stream/orders", org,
		map[string]any{"name": "orders", "subjects": []string{"orders.>"}, "max_msgs": 50})
	_ = json.Unmarshal(body, &st)
	if code != http.StatusOK || st.Config.MaxMsgs != 50 {
		t.Fatalf("update: want 200/max_msgs 50, got %d %+v", code, st.Config)
	}

	code, body = do(t, app, http.MethodPost, "/v1/mq/stream/orders/purge", org, map[string]any{})
	if code != http.StatusOK {
		t.Fatalf("purge: want 200, got %d (%s)", code, body)
	}
	var purged purgeOut
	_ = json.Unmarshal(body, &purged)
	if purged.Purged != 2 {
		t.Fatalf("purged: want 2, got %d", purged.Purged)
	}

	if code, body = do(t, app, http.MethodDelete, "/v1/mq/stream/orders", org, nil); code != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d (%s)", code, body)
	}
	if code, _ = do(t, app, http.MethodGet, "/v1/mq/stream/orders", org, nil); code != http.StatusNotFound {
		t.Fatalf("get after delete: want 404, got %d", code)
	}
}

func TestConsumerPullAcksOnDelivery(t *testing.T) {
	app := plane(t)
	const org = "acme"

	if code, body := do(t, app, http.MethodPost, "/v1/mq/stream", org,
		map[string]any{"name": "jobs", "subjects": []string{"jobs.*"}}); code != http.StatusCreated {
		t.Fatalf("create stream: %d (%s)", code, body)
	}
	code, body := do(t, app, http.MethodPost, "/v1/mq/stream/jobs/consumer", org,
		map[string]any{"durable_name": "worker"})
	if code != http.StatusCreated {
		t.Fatalf("create consumer: want 201, got %d (%s)", code, body)
	}
	var c Consumer
	_ = json.Unmarshal(body, &c)
	if c.Name != "worker" || c.Stream != "jobs" || c.Config.Ack != "explicit" {
		t.Fatalf("consumer presents wrong: %+v", c)
	}

	feed(t, org, "jobs.a", "j1", "j2")

	code, body = do(t, app, http.MethodPost, "/v1/mq/stream/jobs/consumer/worker/next", org,
		map[string]any{"batch": 10, "expires": "5s"})
	if code != http.StatusOK {
		t.Fatalf("next: want 200, got %d (%s)", code, body)
	}
	var pulled readOut
	_ = json.Unmarshal(body, &pulled)
	if len(pulled.Messages) != 2 || pulled.Messages[0].Delivered != 1 {
		t.Fatalf("pull: want 2 delivered-once messages, got %+v", pulled)
	}

	// Acked on delivery: an immediate no_wait pull sees nothing to redeliver.
	code, body = do(t, app, http.MethodPost, "/v1/mq/stream/jobs/consumer/worker/next", org,
		map[string]any{"batch": 10, "no_wait": true})
	_ = json.Unmarshal(body, &pulled)
	if code != http.StatusOK || len(pulled.Messages) != 0 {
		t.Fatalf("re-pull: want 200 with 0 messages, got %d with %d", code, len(pulled.Messages))
	}

	// An empty wait is the 408 the contract names.
	if code, _ = do(t, app, http.MethodPost, "/v1/mq/stream/jobs/consumer/worker/next", org,
		map[string]any{"expires": "1s"}); code != http.StatusRequestTimeout {
		t.Fatalf("empty wait: want 408, got %d", code)
	}

	code, body = do(t, app, http.MethodGet, "/v1/mq/stream/jobs/consumer", org, nil)
	var listing pickOut
	_ = json.Unmarshal(body, &listing)
	if code != http.StatusOK || listing.Total != 1 || listing.Consumers[0].AckFloor.Stream != 2 {
		t.Fatalf("list consumers: want total 1 ack floor 2, got %d (%s)", code, body)
	}

	if code, body = do(t, app, http.MethodDelete, "/v1/mq/stream/jobs/consumer/worker", org, nil); code != http.StatusNoContent {
		t.Fatalf("delete consumer: want 204, got %d (%s)", code, body)
	}
	if code, _ = do(t, app, http.MethodGet, "/v1/mq/stream/jobs/consumer/worker", org, nil); code != http.StatusNotFound {
		t.Fatalf("get after delete: want 404, got %d", code)
	}
}

// TestTenancy: the broker is shared; the surface is not. Another org sees an
// empty product, cannot address the first org's stream by name, and platform
// streams on the same broker are invisible to everyone.
func TestTenancy(t *testing.T) {
	app := plane(t)

	if code, body := do(t, app, http.MethodPost, "/v1/mq/stream", "acme",
		map[string]any{"name": "orders"}); code != http.StatusCreated {
		t.Fatalf("create: %d (%s)", code, body)
	}
	// A platform-internal stream, created directly on the broker like
	// analytics' event plane is.
	nc, err := natsio.Connect(b.nc.ConnectedUrl())
	if err != nil {
		t.Fatalf("independent client: %v", err)
	}
	defer nc.Close()
	js, _ := nc.JetStream()
	if _, err := js.AddStream(&natsio.StreamConfig{Name: "EVENT", Subjects: []string{"event.>"}}); err != nil {
		t.Fatalf("platform stream: %v", err)
	}

	for org, want := range map[string]int{"acme": 1, "rival": 0} {
		code, body := do(t, app, http.MethodGet, "/v1/mq/stream", org, nil)
		var out Streams
		_ = json.Unmarshal(body, &out)
		if code != http.StatusOK || out.Total != want {
			t.Fatalf("%s sees %d streams (want %d): %s", org, out.Total, want, body)
		}
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/mq/stream/orders", "rival", nil); code != http.StatusNotFound {
		t.Fatalf("cross-tenant get: want 404, got %d", code)
	}
	if code, _ := do(t, app, http.MethodGet, "/v1/mq/stream/EVENT", "rival", nil); code != http.StatusNotFound {
		t.Fatalf("platform stream by name: want 404, got %d", code)
	}
	if code, _ := do(t, app, http.MethodDelete, "/v1/mq/stream/orders", "rival", nil); code != http.StatusNotFound {
		t.Fatalf("cross-tenant delete: want 404, got %d", code)
	}
	// No principal, no product.
	if code, _ := do(t, app, http.MethodGet, "/v1/mq/stream", "", nil); code != http.StatusForbidden {
		t.Fatalf("anonymous: want 403, got %d", code)
	}
}

func TestStatus(t *testing.T) {
	app := plane(t)

	code, body := do(t, app, http.MethodGet, "/v1/mq/health", "", nil)
	var h Health
	_ = json.Unmarshal(body, &h)
	if code != http.StatusOK || h.Status != "ok" || h.Version == "" {
		t.Fatalf("health: want ok with a broker version, got %d (%s)", code, body)
	}

	code, body = do(t, app, http.MethodGet, "/v1/mq/info", "acme", nil)
	var i infoOut
	_ = json.Unmarshal(body, &i)
	if code != http.StatusOK || !i.JetStream || i.Server == "" {
		t.Fatalf("info: want jetstream-enabled server identity, got %d (%s)", code, body)
	}
}

// TestDegradedIsHonest: with nothing behind the surface, health says degraded
// and every stream op refuses 503 — nothing pretends.
func TestDegradedIsHonest(t *testing.T) {
	t.Setenv("CLOUD_PUBSUB_URL", "nats://127.0.0.1:1")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	if err := Mount(app, cloud.Deps{}); err != nil {
		t.Fatalf("mount must not need a live broker: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(nil) })

	code, body := do(t, app, http.MethodGet, "/v1/mq/health", "", nil)
	var h Health
	_ = json.Unmarshal(body, &h)
	if code != http.StatusOK || h.Status != "degraded" {
		t.Fatalf("health: want 200 degraded, got %d (%s)", code, body)
	}
	if code, _ = do(t, app, http.MethodGet, "/v1/mq/stream", "acme", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("list without a plane: want 503, got %d", code)
	}
}

// TestNamespaceEncodingIsInjective: two orgs whose ids differ only in bytes
// outside the broker alphabet can never share a namespace.
func TestNamespaceEncodingIsInjective(t *testing.T) {
	pairs := [][2]string{{"a_b", "ab"}, {"a_b", "a-b"}, {"a.b", "a_b"}, {"Ab", "ab"}}
	for _, p := range pairs {
		if tok(p[0]) == tok(p[1]) {
			t.Errorf("tok(%q) == tok(%q) == %q — namespace collision", p[0], p[1], tok(p[0]))
		}
	}
	if got := tok("acme"); got != "acme" {
		t.Errorf("plain org must stay readable: %q", got)
	}
	if fmt.Sprintf("%s", tok("a.b")) != "a_2eb" {
		t.Errorf("escape shape moved: %q", tok("a.b"))
	}
}
