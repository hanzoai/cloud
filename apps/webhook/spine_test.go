package webhook

// spine_test.go — the ONE-STREAM invariant of the platform event plane.
//
// analytics OWNS the plane: it names the stream, it binds event.>, it publishes,
// and its envelope names the tenant `org`. webhooks is a CONSUMER. These tests
// pin that ownership from the consumer side, where a second owner would show up
// as a stream that refuses to be created.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/event"
	"github.com/hanzoai/cloud/internal/mint"
	"github.com/hanzoai/commerce/events"
	"github.com/hanzoai/commerce/infra"
	psembed "github.com/hanzoai/pubsub/embed"
	luxlog "github.com/luxfi/log"
)

// TestOneStreamBindsEventSubjects is the regression that names the defect: JetStream
// refuses a stream whose subjects overlap an existing one, so two subsystems each
// declaring a stream over event.> is not a style problem — it is an outage. The plane
// analytics owns is created FIRST (exactly as event.PublishEvents does it), and
// then every stream this dispatcher consumes must survive being ensured against it.
//
// Before the fix, webhooks' own EVENTS/event.> row failed here with an overlap error.
func TestOneStreamBindsEventSubjects(t *testing.T) {
	srv, err := psembed.Open(psembed.Options{Port: -1, StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("embed pubsub: %v", err)
	}
	defer srv.Shutdown()

	ctx := context.Background()
	cl, err := infra.NewPubSubClient(ctx, &infra.PubSubConfig{URL: srv.ClientURL(), Name: "one-stream", EnableJetStream: true})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = cl.Close() }()

	// The owner creates the plane. This is the analytics stream, under its name.
	if err := event.EnsureEventStream(ctx, cl); err != nil {
		t.Fatalf("analytics must be able to create its own stream: %v", err)
	}

	// Every stream the dispatcher consumes, ensured the way consume() ensures it.
	for _, s := range streams {
		if err := cl.EnsureStream(ctx, &infra.StreamConfig{Name: s.stream, Subjects: s.subjects}); err != nil {
			t.Fatalf("stream %s (%v) cannot coexist with the plane analytics owns: %v\n"+
				"two streams binding the same subjects is a JetStream overlap error — "+
				"exactly one subsystem may declare event.>", s.stream, s.subjects, err)
		}
	}

	// And state the invariant directly: no consumed row may re-declare event.> under
	// a name of its own. Consuming analytics' stream is by NAME, never by re-binding.
	for _, s := range streams {
		for _, sub := range s.subjects {
			if strings.HasPrefix(sub, "event.") && s.stream != event.EventStream {
				t.Fatalf("stream %q binds %q — the event plane is event.EventStream (%q); "+
					"webhooks must consume it, not declare a second owner",
					s.stream, sub, event.EventStream)
			}
		}
	}
}

// TestAnalyticsEventReachesWebhook is the whole spine, in the ONE direction it now
// runs: analytics publishes an accepted batch onto ITS stream with ITS envelope, and
// this dispatcher — a durable consumer, the same machinery commerce.> rides — resolves
// the tenant from that envelope and delivers a signed POST to the org's endpoint.
//
// Before the fix, an analytics-published event carried `org` while orgOf read only
// `organization_id`, so the tenant resolved to "" and the event was delivered to nobody.
func TestAnalyticsEventReachesWebhook(t *testing.T) {
	srv, err := psembed.Open(psembed.Options{Port: -1, StoreDir: t.TempDir()})
	if err != nil {
		t.Fatalf("embed pubsub: %v", err)
	}
	defer srv.Shutdown()

	received := make(chan []byte, 4)
	sub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer sub.Close()

	stores := cloud.NewOrgStore[*store](cloud.Base{DataDir: t.TempDir()}, "webhooks", openStore)
	defer func() { _ = stores.CloseAll() }()
	d := newDispatcher(stores, luxlog.New("test"))
	st, err := stores.For(cloud.MustOrgNamespace("acme", ""))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := st.create(context.Background(), Endpoint{
		ID: mint.ID("wh"), Org: "acme", URL: sub.URL, Events: []string{"event.>"},
		Secret: "whsec_spine", Status: "active", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	// ONE knob: the bus both halves read is the pubsub export.
	t.Setenv("CLOUD_PUBSUB_URL", srv.ClientURL())
	d.start()
	defer d.stop()

	batch := []event.SinkEvent{{
		MessageID:  "m1",
		Name:       "$pageview",
		DistinctID: "d1",
		Time:       time.Now().UTC(),
		Properties: map[string]any{"path": "/pricing"},
	}}
	deadline := time.After(20 * time.Second)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		// The publisher is ANALYTICS. webhooks holds no publish path at all.
		event.PublishEvents("acme", batch)
		select {
		case body := <-received:
			var env event.EventEnvelope
			if err := json.Unmarshal(body, &env); err != nil {
				t.Fatalf("delivered body is not the analytics envelope: %v (%s)", err, body)
			}
			if env.Org != "acme" || env.Name != "$pageview" || env.DistinctID != "d1" {
				t.Fatalf("envelope = %+v — org, name and person must survive the spine", env)
			}
			return
		case <-tick.C:
		case <-deadline:
			t.Fatal("event never crossed the spine (analytics publish -> bus -> webhook)")
		}
	}
}

// TestCommerceDispatchUnaffected pins that unifying the event plane did not disturb
// the OTHER envelope on the bus: a commerce event names its tenant organization_id,
// and it must still resolve, match and queue exactly as before.
func TestCommerceDispatchUnaffected(t *testing.T) {
	d := newTestDispatcher(t)
	seedEndpoint(t, d, "acme", Endpoint{URL: "https://acme.test/h", Secret: "sk", Events: []string{"commerce.>"}})

	m := &infra.StreamMessage{Subject: events.SubjectOrderCreated, Data: commerceEvent(t, "acme")}
	if err := d.handle(context.Background(), commerceSource(), m); err != nil {
		t.Fatalf("handle: %v", err)
	}
	jobs := drainJobs(d)
	if len(jobs) != 1 || jobs[0].org != "acme" || jobs[0].secret != "sk" {
		t.Fatalf("commerce event must still queue the org's endpoint, got %+v", jobs)
	}
}

// TestWarehouseFactsAreNotDeliveredAsEnvelopes pins the OTHER vocabulary on the event
// plane. Two things publish there: the subscriber envelope this package delivers, and
// the warehouse's facts. Their subjects OVERLAP — a product event named "$error" folds
// onto event.error, which is also the error signal's subject, and "$error" is what a
// browser error with no name of its own is called — so subject matching alone cannot
// tell them apart, and a wildcard subscriber (event.>) matches both.
//
// Delivering a fact would send a subscriber a body in a shape its endpoint has never
// been promised, and send it a SECOND time for an event already delivered. Both
// messages below carry org=acme and land on a subject this endpoint matches; exactly
// one of them is this package's to deliver.
func TestWarehouseFactsAreNotDeliveredAsEnvelopes(t *testing.T) {
	d := newTestDispatcher(t)
	seedEndpoint(t, d, "acme", Endpoint{URL: "https://acme.test/h", Secret: "sk", Events: []string{"event.>"}})
	src := analyticsSource()

	fact := []byte(`{"signal":"error","org":"acme","id":"f1","name":"ChunkLoadError"}`)
	if err := d.handle(context.Background(), src, &infra.StreamMessage{Subject: "event.error", Data: fact}); err != nil {
		t.Fatalf("handle fact: %v — a fact is acked, never naked into a redelivery loop", err)
	}
	if jobs := drainJobs(d); len(jobs) != 0 {
		t.Fatalf("queued %d deliveries for a warehouse fact: %+v", len(jobs), jobs)
	}

	env := []byte(`{"org":"acme","id":"e1","name":"$error","distinct_id":"d1"}`)
	if err := d.handle(context.Background(), src, &infra.StreamMessage{Subject: "event.error", Data: env}); err != nil {
		t.Fatalf("handle envelope: %v", err)
	}
	jobs := drainJobs(d)
	if len(jobs) != 1 || jobs[0].org != "acme" {
		t.Fatalf("the subscriber envelope on the SAME subject must still deliver, got %+v", jobs)
	}
}

// analyticsSource is the streams row carrying event.> — the plane analytics owns.
func analyticsSource() streamSource {
	for _, s := range streams {
		if s.stream == event.EventStream {
			return s
		}
	}
	panic("the event plane is no longer consumed")
}

// commerceSource is the streams row carrying commerce.> — the one whose envelope
// names the tenant organization_id.
func commerceSource() streamSource {
	for _, s := range streams {
		if s.stream == events.StreamName {
			return s
		}
	}
	panic("commerce stream is no longer consumed")
}
