package analytics

import (
	"testing"
	"time"
)

// TestFanOutTranslatesAndFiresSink verifies the fan-out seam builds SinkEvents from
// the RAW accepted batch (resolved name, commerce fields, un-scrubbed properties for
// match keys), drops unroutable events, and dispatches them to the installed sink.
func TestFanOutTranslatesAndFiresSink(t *testing.T) {
	got := make(chan []SinkEvent, 1)
	SetSink(func(org string, evs []SinkEvent) {
		if org == "acme" {
			got <- evs
		}
	})
	defer SetSink(nil)

	fanOut("acme", []CaptureEvent{
		{Type: "event", Event: "order_completed", DistinctID: "u1", Revenue: 49, Currency: "USD",
			Properties: map[string]any{"email": "a@b.com"}},
		{Type: "pageview"},         // resolves to $pageview
		{Type: "event", Event: ""}, // unroutable → dropped (mirrors the write core)
	})

	select {
	case evs := <-got:
		if len(evs) != 2 {
			t.Fatalf("want 2 routable events, got %d", len(evs))
		}
		if evs[0].Name != "order_completed" || evs[0].Revenue != 49 || evs[0].Currency != "USD" {
			t.Errorf("purchase event not carried: %+v", evs[0])
		}
		if evs[0].Properties["email"] != "a@b.com" {
			t.Errorf("raw (pre-scrub) properties must be carried for match keys: %+v", evs[0].Properties)
		}
		if evs[1].Name != "$pageview" {
			t.Errorf("pageview name = %q", evs[1].Name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("sink was not called")
	}
}

// TestFanOutNilSinkIsNoOp verifies fan-out is inert (no panic, no goroutine) when no
// sink is installed — the default when destinations is disabled.
func TestFanOutNilSinkIsNoOp(t *testing.T) {
	SetSink(nil)
	fanOut("acme", []CaptureEvent{{Type: "event", Event: "order_completed"}})
	// Nothing to assert beyond "did not panic / block".
}
