package webhooks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/commerce/events"
	"github.com/hanzoai/commerce/infra"
	luxlog "github.com/luxfi/log"
	psembed "github.com/hanzoai/pubsub/embed"
)

// newTestDispatcher builds a dispatcher over a temp per-org store with the retry sleep
// stubbed out (so the retry ladder runs instantly). Workers are NOT started; the
// handle-level tests call deliver/handle directly.
func newTestDispatcher(t *testing.T) *dispatcher {
	t.Helper()
	stores := cloud.NewOrgStore[*store](t.TempDir(), "webhooks", openStore)
	t.Cleanup(func() { _ = stores.CloseAll() })
	d := newDispatcher(stores, luxlog.New("test"))
	d.sleep = func(time.Duration) {}
	return d
}

// seedEndpoint inserts an endpoint directly into org's store (bypassing the HTTP layer).
func seedEndpoint(t *testing.T, d *dispatcher, org string, e Endpoint) {
	t.Helper()
	st, err := d.stores.For(org, "")
	if err != nil {
		t.Fatalf("open store for %s: %v", org, err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	e.Org, e.CreatedAt, e.UpdatedAt = org, now, now
	if e.ID == "" {
		e.ID = newID("wh")
	}
	if e.Status == "" {
		e.Status = "active"
	}
	if err := st.create(context.Background(), e); err != nil {
		t.Fatalf("seed endpoint: %v", err)
	}
}

// drainJobs non-blockingly reads every queued job.
func drainJobs(d *dispatcher) []deliveryJob {
	var out []deliveryJob
	for {
		select {
		case j := <-d.jobs:
			out = append(out, j)
		default:
			return out
		}
	}
}

// commerceEvent marshals a commerce envelope with the given org (organization_id).
func commerceEvent(t *testing.T, org string) []byte {
	t.Helper()
	b, err := json.Marshal(events.CommerceEvent{
		ID:             "ev_1",
		Type:           "order.created",
		OrganizationID: org,
		Data:           map[string]any{"orderId": "ord_1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// capture records what a subscriber received.
type capture struct {
	calls  int32
	sigHdr string
	event  string
	deliv  string
	body   []byte
}

// TestDeliverHeadersAndSignature proves ONE successful delivery carries the canonical
// headers and a signature a subscriber can recompute over (t, body) with the shared secret.
func TestDeliverHeadersAndSignature(t *testing.T) {
	d := newTestDispatcher(t)
	var cap capture
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&cap.calls, 1)
		cap.sigHdr = r.Header.Get("X-Webhook-Signature")
		cap.event = r.Header.Get("X-Webhook-Event")
		cap.deliv = r.Header.Get("X-Webhook-Delivery")
		cap.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	body := []byte(`{"organization_id":"acme","type":"order.created"}`)
	job := deliveryJob{url: srv.URL, secret: "whsec_test", subject: "commerce.order.created", delivery: newUUID(), body: body}
	d.deliver(context.Background(), job)

	if got := atomic.LoadInt32(&cap.calls); got != 1 {
		t.Fatalf("want exactly 1 delivery attempt, got %d", got)
	}
	if cap.event != "commerce.order.created" {
		t.Fatalf("X-Webhook-Event = %q", cap.event)
	}
	if cap.deliv != job.delivery {
		t.Fatalf("X-Webhook-Delivery = %q, want %q", cap.deliv, job.delivery)
	}
	if string(cap.body) != string(body) {
		t.Fatalf("subscriber body mismatch: %q", cap.body)
	}
	// Recompute the signature exactly as a subscriber would.
	ts, v1 := parseSig(t, cap.sigHdr)
	if want := signPayload("whsec_test", ts, body); v1 != want {
		t.Fatalf("signature mismatch: header v1=%s, recomputed=%s", v1, want)
	}
}

// TestDeliverRetryOn500 proves a 500 then 200 delivers after exactly 2 attempts.
func TestDeliverRetryOn500(t *testing.T) {
	d := newTestDispatcher(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d.deliver(context.Background(), deliveryJob{url: srv.URL, secret: "s", subject: "commerce.order.created", delivery: newUUID(), body: []byte("{}")})
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("want 2 attempts (500 then 200), got %d", got)
	}
}

// TestDeliverPermanentOn4xx proves a 4xx (not 429) is permanent — NO retry.
func TestDeliverPermanentOn4xx(t *testing.T) {
	d := newTestDispatcher(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	d.deliver(context.Background(), deliveryJob{url: srv.URL, secret: "s", subject: "commerce.order.created", delivery: newUUID(), body: []byte("{}")})
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("4xx must be permanent (1 attempt), got %d", got)
	}
}

// TestDeliverExhaustsRetries proves a persistent 500 stops after maxAttempts.
func TestDeliverExhaustsRetries(t *testing.T) {
	d := newTestDispatcher(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	d.deliver(context.Background(), deliveryJob{url: srv.URL, secret: "s", subject: "commerce.order.created", delivery: newUUID(), body: []byte("{}")})
	if got := atomic.LoadInt32(&calls); got != maxAttempts {
		t.Fatalf("want %d attempts on persistent 5xx, got %d", maxAttempts, got)
	}
}

// TestHandleMatchesAndQueues proves handle queues ONE job for a matching active endpoint,
// carrying the endpoint's secret + the event's raw body + subject.
func TestHandleMatchesAndQueues(t *testing.T) {
	d := newTestDispatcher(t)
	seedEndpoint(t, d, "acme", Endpoint{URL: "https://acme.test/h", Secret: "sk", Events: []string{"commerce.>"}})

	body := commerceEvent(t, "acme")
	if err := d.handle(context.Background(), &infra.StreamMessage{Subject: "commerce.order.created", Data: body}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	jobs := drainJobs(d)
	if len(jobs) != 1 {
		t.Fatalf("want 1 queued job, got %d", len(jobs))
	}
	if jobs[0].secret != "sk" || jobs[0].subject != "commerce.order.created" || string(jobs[0].body) != string(body) {
		t.Fatalf("queued job mismatch: %+v", jobs[0])
	}
}

// TestHandleSkipsDisabled proves a disabled endpoint is never delivered to.
func TestHandleSkipsDisabled(t *testing.T) {
	d := newTestDispatcher(t)
	seedEndpoint(t, d, "acme", Endpoint{URL: "https://acme.test/h", Secret: "sk", Events: []string{"commerce.>"}, Status: "disabled"})

	if err := d.handle(context.Background(), &infra.StreamMessage{Subject: "commerce.order.created", Data: commerceEvent(t, "acme")}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if jobs := drainJobs(d); len(jobs) != 0 {
		t.Fatalf("disabled endpoint must not be queued, got %d jobs", len(jobs))
	}
}

// TestHandleNonMatchingFiltered proves an event whose subject matches no pattern queues nothing.
func TestHandleNonMatchingFiltered(t *testing.T) {
	d := newTestDispatcher(t)
	seedEndpoint(t, d, "acme", Endpoint{URL: "https://acme.test/h", Secret: "sk", Events: []string{"commerce.order.*"}})

	if err := d.handle(context.Background(), &infra.StreamMessage{Subject: "commerce.payment.received", Data: commerceEvent(t, "acme")}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if jobs := drainJobs(d); len(jobs) != 0 {
		t.Fatalf("non-matching subject must queue nothing, got %d jobs", len(jobs))
	}
}

// TestHandleOrgIsolationOfDelivery proves org B's endpoint NEVER receives org A's event:
// an event tagged org A matches ONLY A's subscriptions.
func TestHandleOrgIsolationOfDelivery(t *testing.T) {
	d := newTestDispatcher(t)
	// B subscribes to everything.
	seedEndpoint(t, d, "borg", Endpoint{URL: "https://borg.test/h", Secret: "b", Events: nil})
	// A subscribes to everything.
	seedEndpoint(t, d, "acme", Endpoint{URL: "https://acme.test/h", Secret: "a", Events: nil})

	// An event emitted by A must queue ONLY A's endpoint.
	if err := d.handle(context.Background(), &infra.StreamMessage{Subject: "commerce.order.created", Data: commerceEvent(t, "acme")}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	jobs := drainJobs(d)
	if len(jobs) != 1 || jobs[0].secret != "a" {
		t.Fatalf("event for A must queue ONLY A's endpoint, got %+v", jobs)
	}
}

// TestHandleNoOrgDeliversToNobody proves an envelope with no org queues nothing (ACK, no delivery).
func TestHandleNoOrgDeliversToNobody(t *testing.T) {
	d := newTestDispatcher(t)
	seedEndpoint(t, d, "acme", Endpoint{URL: "https://acme.test/h", Secret: "sk", Events: nil})

	if err := d.handle(context.Background(), &infra.StreamMessage{Subject: "commerce.order.created", Data: []byte(`{"type":"order.created"}`)}); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if jobs := drainJobs(d); len(jobs) != 0 {
		t.Fatalf("org-less event must queue nothing, got %d jobs", len(jobs))
	}
}

// TestBusEndToEnd runs an in-process NATS+JetStream server, mounts the dispatcher against
// it, publishes a commerce event to the COMMERCE stream, and asserts the org's subscriber
// endpoint receives the signed POST — the full registry→bus→delivery path.
func TestBusEndToEnd(t *testing.T) {
	srv, err := psembed.Open(psembed.Options{Port: -1, StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("embed nats: %v", err)
	}
	defer srv.Shutdown()
	url := srv.ClientURL()

	received := make(chan capture, 4)
	sub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- capture{
			sigHdr: r.Header.Get("X-Webhook-Signature"),
			event:  r.Header.Get("X-Webhook-Event"),
			deliv:  r.Header.Get("X-Webhook-Delivery"),
			body:   body,
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer sub.Close()

	stores := cloud.NewOrgStore[*store](t.TempDir(), "webhooks", openStore)
	defer func() { _ = stores.CloseAll() }()
	d := newDispatcher(stores, luxlog.New("test"))
	// Seed an active subscriber for org "acme".
	st, err := stores.For("acme", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := st.create(context.Background(), Endpoint{
		ID: newID("wh"), Org: "acme", URL: sub.URL, Events: []string{"commerce.>"},
		Secret: "whsec_e2e", Status: "active", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	t.Setenv(natsURLEnv, url)
	d.start()
	defer d.stop()

	// Publisher: ensure the stream, then publish until the subscriber receives (the
	// durable consumer starts from DeliverNew, so we publish until it is bound).
	pub, err := infra.NewPubSubClient(context.Background(), &infra.PubSubConfig{URL: url, Name: "test-pub", EnableJetStream: true})
	if err != nil {
		t.Fatalf("publisher: %v", err)
	}
	defer func() { _ = pub.Close() }()
	if err := pub.EnsureStream(context.Background(), &infra.StreamConfig{Name: events.StreamName, Subjects: events.StreamSubjects}); err != nil {
		t.Fatalf("ensure stream: %v", err)
	}
	eventBody := commerceEvent(t, "acme")

	deadline := time.After(15 * time.Second)
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := pub.PublishToStream(context.Background(), events.SubjectOrderCreated, eventBody); err != nil {
			t.Fatalf("publish: %v", err)
		}
		select {
		case got := <-received:
			if got.event != events.SubjectOrderCreated {
				t.Fatalf("X-Webhook-Event = %q, want %q", got.event, events.SubjectOrderCreated)
			}
			if got.deliv == "" {
				t.Fatal("missing X-Webhook-Delivery")
			}
			ts, v1 := parseSig(t, got.sigHdr)
			if want := signPayload("whsec_e2e", ts, got.body); v1 != want {
				t.Fatalf("e2e signature mismatch: v1=%s recomputed=%s", v1, want)
			}
			return
		case <-tick.C:
			continue
		case <-deadline:
			t.Fatal("subscriber never received the delivered event within 15s")
		}
	}
}

// TestDeliverRecordsPerAttempt proves the delivery log captures ONE row per attempt across
// a retry: a 500 then 200 writes attempt 1 "retrying" (http 500) and attempt 2 "ok" (http
// 200), newest first, both sharing the group's delivery id.
func TestDeliverRecordsPerAttempt(t *testing.T) {
	d := newTestDispatcher(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	deliv := newUUID()
	job := deliveryJob{org: "acme", endpointID: "wh_log", url: srv.URL, secret: "sk", subject: "commerce.order.created", delivery: deliv, body: []byte("{}")}
	d.deliver(context.Background(), job)

	st, err := d.stores.For("acme", "")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	rows, err := st.deliveries(context.Background(), "wh_log", 50, "")
	if err != nil {
		t.Fatalf("deliveries: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 delivery rows (one per attempt), got %d: %+v", len(rows), rows)
	}
	// newest first: attempt 2 (ok, 200), then attempt 1 (retrying, 500).
	if rows[0].Attempt != 2 || rows[0].Status != "ok" || rows[0].HTTPStatus != 200 {
		t.Fatalf("row[0] want attempt 2 ok/200, got %+v", rows[0])
	}
	if rows[1].Attempt != 1 || rows[1].Status != "retrying" || rows[1].HTTPStatus != 500 {
		t.Fatalf("row[1] want attempt 1 retrying/500, got %+v", rows[1])
	}
	for _, r := range rows {
		if r.DeliveryID != deliv {
			t.Fatalf("delivery id mismatch: %q vs %q", r.DeliveryID, deliv)
		}
		if r.Subject != "commerce.order.created" {
			t.Fatalf("subject = %q", r.Subject)
		}
	}
}

// TestDeliveryRetentionPrune proves recordDelivery caps the per-endpoint log at
// maxDeliveryRowsPerEndpoint, keeping the newest rows and pruning the oldest on insert.
func TestDeliveryRetentionPrune(t *testing.T) {
	d := newTestDispatcher(t)
	st, err := d.stores.For("acme", "")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ctx := context.Background()
	total := maxDeliveryRowsPerEndpoint + 20
	for i := 0; i < total; i++ {
		if err := st.recordDelivery(ctx, DeliveryRow{
			EndpointID: "wh_ret", DeliveryID: newUUID(), Subject: "s", Attempt: i,
			Status: "ok", HTTPStatus: 200, Created: time.Now().UTC().Format(time.RFC3339),
		}); err != nil {
			t.Fatalf("record %d: %v", i, err)
		}
	}
	rows, err := st.deliveries(ctx, "wh_ret", maxDeliveryRowsPerEndpoint+100, "")
	if err != nil {
		t.Fatalf("deliveries: %v", err)
	}
	if len(rows) != maxDeliveryRowsPerEndpoint {
		t.Fatalf("retention: want %d rows, got %d", maxDeliveryRowsPerEndpoint, len(rows))
	}
	// The newest insert (Attempt == total-1) survives; everything older than the cap is gone.
	if rows[0].Attempt != total-1 {
		t.Fatalf("newest surviving row Attempt = %d, want %d", rows[0].Attempt, total-1)
	}
	for _, r := range rows {
		if r.Attempt < total-maxDeliveryRowsPerEndpoint {
			t.Fatalf("stale row survived prune: Attempt %d", r.Attempt)
		}
	}
}

// ---- test helpers ----

// parseSig splits a `t=<unix>,v1=<hex>` header into its parts.
func parseSig(t *testing.T, header string) (int64, string) {
	t.Helper()
	var ts int64
	var v1 string
	for _, part := range strings.Split(header, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "t":
			n, err := strconv.ParseInt(kv[1], 10, 64)
			if err != nil {
				t.Fatalf("bad t in signature header %q: %v", header, err)
			}
			ts = n
		case "v1":
			v1 = kv[1]
		}
	}
	if ts == 0 || v1 == "" {
		t.Fatalf("signature header missing t or v1: %q", header)
	}
	return ts, v1
}
