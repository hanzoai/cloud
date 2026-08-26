package o11y

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// The address answers with no credentials, through the REAL mount — route table,
// Bridge, typed op and gate — because the question a caller asks here is asked
// before they have a session, and often while the platform is on fire.
func TestSignalsAnswerAnonymously(t *testing.T) {
	app := doorApp(t)

	code, body := get(t, app, "/v1/o11y/signals")
	if code != http.StatusOK {
		t.Fatalf("anonymous GET /v1/o11y/signals = %d %s, want 200 — it carries no tenant's data "+
			"and there is nothing to scope", code, body)
	}
	var got Telemetry
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	if len(got.Signals) != 3 {
		t.Fatalf("carries %d signals, want the three this plane holds: %s", len(got.Signals), body)
	}
	want := map[string]string{
		"metric": "event.metric",
		"log":    "event.log",
		"span":   "event.span",
	}
	for _, s := range got.Signals {
		store, named := want[s.Name]
		if !named {
			t.Errorf("signal %q is not one of the three", s.Name)
			continue
		}
		if s.Store != store {
			t.Errorf("%s is stored at %q, want %q — the answer's whole content is which store holds it",
				s.Name, s.Store, store)
		}
		if !strings.HasPrefix(s.Read, "/v1/o11y/") {
			t.Errorf("%s is read at %q, want an address under this capability's own name", s.Name, s.Read)
		}
		delete(want, s.Name)
	}
	if len(want) > 0 {
		t.Errorf("missing: %v", want)
	}
}

// The stores named here are the ones the plane sink actually writes. Naming a
// store nothing writes to would be a document that reads correct and points a
// reader at an empty table, which is the failure the retired probes shipped for
// years — they reported an in-memory map that held nothing.
func TestSignalsNameTheStoresTheSinkWritesTo(t *testing.T) {
	written := map[string]bool{planeLogTable: true, planeSpanTable: true}
	for _, s := range telemetry.Signals {
		if s.Name == "metric" {
			continue // pushed by metricspush.go, not by the ZAP sink
		}
		if !written[s.Store] {
			t.Errorf("signal %q names %q, which this app's plane sink does not write (it writes %s and %s)",
				s.Name, s.Store, planeSpanTable, planeLogTable)
		}
	}
}
