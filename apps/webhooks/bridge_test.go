package webhooks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/analytics"
	psembed "github.com/hanzoai/pubsub/embed"
	luxlog "github.com/luxfi/log"
)

// TestSubjectForFoldsNamesToTokens pins the subject grammar: canonical names
// land on their token, arbitrary names fold safely, and nothing can mint a
// wildcard or an unbounded token.
func TestSubjectForFoldsNamesToTokens(t *testing.T) {
	cases := map[string]string{
		"$pageview":       "event.pageview",
		"$error":          "event.error",
		"$identify":       "event.identify",
		"Signed Up":       "event.signed_up",
		"order.completed": "event.order_completed",
		"a>b*c":           "event.a_b_c",
		"$":               "event.custom",
		"":                "event.custom",
		"...":             "event.custom",
	}
	for name, want := range cases {
		if got := subjectFor(name); got != want {
			t.Errorf("subjectFor(%q) = %q, want %q", name, got, want)
		}
	}
	long := subjectFor("xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx")
	if len(long) > len("event.")+48 {
		t.Errorf("subjectFor must bound token length, got %d bytes", len(long))
	}
}

// TestSpineEndToEnd drives the WHOLE spine in-process: an accepted event batch
// enters the fan-out seam (analytics.AddSink — exactly what the door's write
// core calls), the bridge publishes it onto the EVENTS stream of an embedded
// pubsub, and the SAME dispatcher delivers it, signed, to an org webhook
// subscribed to the event.> pattern — with the org resolved from the
// envelope's organization_id, never from the subscriber's claim.
func TestSpineEndToEnd(t *testing.T) {
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

	stores := cloud.NewOrgStore[*store](t.TempDir(), "webhooks", openStore)
	defer func() { _ = stores.CloseAll() }()
	d := newDispatcher(stores, luxlog.New("test"))
	st, err := stores.For("acme", "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if err := st.create(context.Background(), Endpoint{
		ID: newID("wh"), Org: "acme", URL: sub.URL, Events: []string{"event.>"},
		Secret: "whsec_spine", Status: "active", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	t.Setenv(natsURLEnv, srv.ClientURL())
	d.start()
	defer d.stop()
	remove := analytics.AddSink(d.publishEvents)
	defer remove()

	// Wait for the dispatcher's bus client, then feed the seam until delivery
	// (the durable consumer is DeliverNew, so publish repeatedly until bound).
	batch := []analytics.SinkEvent{{
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
		d.publishEvents("acme", batch)
		select {
		case body := <-received:
			var env Envelope
			if err := json.Unmarshal(body, &env); err != nil {
				t.Fatalf("delivered body is not the Envelope: %v (%s)", err, body)
			}
			if env.OrganizationID != "acme" || env.Name != "$pageview" || env.DistinctID != "d1" {
				t.Fatalf("envelope = %+v — org, name and person must survive the spine", env)
			}
			return
		case <-tick.C:
		case <-deadline:
			t.Fatal("event never crossed the spine (seam -> bus -> webhook)")
		}
	}
}

// TestPublishEventsNoBusIsNoOp: the bridge with no live bus client publishes
// nothing and never blocks — the warehouse copy is the durable one.
func TestPublishEventsNoBusIsNoOp(t *testing.T) {
	stores := cloud.NewOrgStore[*store](t.TempDir(), "webhooks", openStore)
	defer func() { _ = stores.CloseAll() }()
	d := newDispatcher(stores, luxlog.New("test"))
	done := make(chan struct{})
	go func() {
		d.publishEvents("acme", []analytics.SinkEvent{{MessageID: "m", Name: "$pageview"}})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publishEvents blocked with no bus client")
	}
}
