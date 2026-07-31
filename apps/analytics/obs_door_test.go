package analytics

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
)

// TestObsPlaneGetsFirstRefusalOnTheCanonicalDoor pins the unified event door:
// POST /v1/event is ONE door for every event kind. An authenticated body the
// o11y plane claims (an LLM-obs ingestion batch) is consumed by the claim with
// the SERVER-resolved org and never reaches the product warehouse; a body the
// claim declines walks the product wire exactly as before; and only the
// canonical door's FULL lane offers — the other doors and the anonymous lane
// never consult the claim (obs events are tenant data).
func TestObsPlaneGetsFirstRefusalOnTheCanonicalDoor(t *testing.T) {
	type call struct{ org, body string }
	var calls []call
	prev := cloud.ObsEventIngest()
	cloud.SetObsEventIngest(func(_ context.Context, org string, body []byte) (int, int, bool, error) {
		calls = append(calls, call{org: org, body: string(body)})
		if strings.Contains(string(body), `"type":"trace-create"`) {
			return 1, 0, true, nil
		}
		return 0, 0, false, nil
	})
	t.Cleanup(func() { cloud.SetObsEventIngest(prev) })

	obsBody := `{"batch":[{"id":"a","type":"trace-create","timestamp":"t","body":{}}]}`

	// Claimed: the obs receipt answers, the warehouse sees nothing.
	w := fakeWarehouse(t)
	app := mountApp(t)
	code, body := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme", obsBody)
	if code != http.StatusOK || !strings.Contains(string(body), `"accepted":1`) {
		t.Fatalf("claimed obs batch = %d (%s), want 200 with the claim's receipt", code, body)
	}
	if len(calls) != 1 || calls[0].org != "acme" {
		t.Fatalf("claim calls = %+v, want exactly one with the server-resolved org", calls)
	}
	if got := w.sources(t); len(got) != 0 {
		t.Fatalf("claimed batch leaked into the product warehouse: %v", got)
	}

	// Declined: the product wire proceeds untouched.
	calls = nil
	if code, _ := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme",
		`{"event":"$pageview","distinctId":"d"}`); code != http.StatusOK {
		t.Fatalf("declined product event = %d, want 200 via the product wire", code)
	}
	if len(calls) != 1 {
		t.Fatalf("the claim must be OFFERED the declined body exactly once, got %d", len(calls))
	}
	if got := w.sources(t); len(got) != 1 || got[0] != sourceEvent {
		t.Fatalf("product wire wrote $source %v, want [%s]", got, sourceEvent)
	}

	// Other doors never offer: the same obs body on /v1/insights/e walks that
	// door's own wire without consulting the claim. The offer is gated on the
	// door's SOURCE, not on the body, which is why the PostHog door is the honest
	// negative — it is the only other door there is.
	calls = nil
	if code, _ := doBody(t, app, http.MethodPost, "/v1/insights/e", "user-dave", "acme", obsBody); code != http.StatusOK {
		t.Fatal("obs-shaped body on a non-canonical door must still answer via its own wire")
	}
	if len(calls) != 0 {
		t.Fatalf("non-canonical door consulted the claim %d times, want 0", len(calls))
	}

	// The anonymous lane never offers — obs events are tenant data.
	calls = nil
	tightenPublicRate(t, 1_000_000, 1_000_000)
	if code, _ := doBody(t, app, http.MethodPost, "/v1/event", "", "", obsBody); code != http.StatusOK {
		t.Fatal("anonymous canonical-door post must still answer via the public projection")
	}
	if len(calls) != 0 {
		t.Fatalf("anonymous lane consulted the claim %d times, want 0", len(calls))
	}
}

// TestTeamWireRidesTheCanonicalDoor pins the team-wire fold: the
// team SPA's bare snake_case array, POSTed to the CANONICAL door, decodes via
// the team mapping (kind named, events survive admission) — and the canonical
// array wire still decodes as itself (positive-signal dispatch only).
func TestTeamWireRidesTheCanonicalDoor(t *testing.T) {
	team := `[{"event":"navigation","properties":{"path":"/x"},"timestamp":1753900000000,"distinct_id":"acct-1"}]`
	evs, err := decodeIngest([]byte(team))
	if err != nil || len(evs) != 1 {
		t.Fatalf("decodeIngest(team array) = %d events, %v; want 1, nil", len(evs), err)
	}
	if evs[0].Type != "pageview" || evs[0].DistinctID != "acct-1" || evs[0].Timestamp == "" {
		t.Fatalf("team element decoded as %+v — the team mapping (kind, snake id, ms time) must apply", evs[0])
	}

	canonical := `[{"event":"$pageview","distinctId":"d","time":"2026-01-01T00:00:00Z"}]`
	evs, err = decodeIngest([]byte(canonical))
	if err != nil || len(evs) != 1 || evs[0].DistinctID != "d" {
		t.Fatalf("canonical array must still decode canonically, got %+v (%v)", evs, err)
	}
}
