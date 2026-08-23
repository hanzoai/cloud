package event

import (
	"net/http"
	"testing"
)

// TestObsShapedBodyIsRefusedNotAbsorbed pins the unified event endpoint: POST
// /v1/event is ONE endpoint for every event kind, and there is nothing in front
// of it.
//
// There used to be. The observability plane got FIRST REFUSAL on every
// authenticated body here — a plane op (obs_event_claim) that took the ones which
// were LLM-obs ingestion batches and declined the rest. Both the claim and the sink
// behind it are gone: that sink inserted UNQUALIFIED `traces`/`observations`/
// `scores` over a DSN naming no database, so the names resolved to `default`, where
// they have never existed. It could only ever decline. So this test no longer has
// two worlds to distinguish — "peer present" was never a reachable state — and what
// it pins is the single one that remains: an obs-shaped body is ORDINARY here, and
// the endpoint owes it the same honest receipt as anything else.
//
// THE ASSERTION BELOW USED TO READ `want 200 via the product wire`, and 200 is what
// it got — while storing zero facts. The trace was gone and the endpoint said
// fine, which is precisely how an o11y outage runs for months without anyone
// seeing it: the only thing that changes is a number in a receipt nobody reads. A
// test that asserts the STATUS of a body that lands nothing certifies the loss. So
// this one asserts BOTH halves — the refusal AND the empty warehouse — because
// either alone is the bug: a 400 with facts landed would be a lie in the other
// direction.
func TestObsShapedBodyIsRefusedNotAbsorbed(t *testing.T) {
	w := fakeWarehouse(t)
	app := mountApp(t)

	// An LLM-obs-SHAPED body. The canonical decode names no event in it — a trace
	// envelope carries `type`, never the `event` that makes a fact landable — so
	// nothing is routable and the caller, who holds a FULL credential, is told the
	// body is why (400), never that a key is missing (401).
	obsBody := `{"batch":[{"id":"a","type":"trace-create","timestamp":"t","body":{}}]}`
	code, body := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme", obsBody)
	refused(t, "obs-shaped body", code, body, http.StatusBadRequest, "unroutable_events")
	if len(w.facts) != 0 {
		t.Fatalf("obs-shaped body landed %d facts; it lands none, which is the whole reason "+
			"the receipt has to say so", len(w.facts))
	}

	// And the product wire is INTACT beside it. This is the pairing that makes the
	// refusal above a statement about the BODY rather than about a broken endpoint:
	// the same endpoint, the same credential, one shape refused and the other stored.
	if code, got := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme",
		`{"event":"$pageview","distinctId":"d"}`); code != http.StatusOK {
		t.Fatalf("product event = %d (%s), want 200", code, got)
	}
	if got := w.sources(t); len(got) == 0 {
		t.Fatal("the product wire must still WRITE; nothing reached the warehouse")
	}
}

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

// TestPostHogWireRidesTheCanonicalDoor pins the other half of retiring
// /v1/event/insights/e: the endpoint was removed, but until the canonical decode
// learned this wire's shape, a PostHog body landing on /v1/event decoded with an
// EMPTY person and an unnamed kind — which admitPublic drops whole, so the SDK
// saw a 200 that stored nothing. insights.hanzo.ai's /e, /batch and /capture all
// rewrite onto /v1/event, so this is the live path for every PostHog SDK.
func TestPostHogWireRidesTheCanonicalDoor(t *testing.T) {
	// A bare PostHog event: snake_case person, string timestamp.
	evs, err := decodeIngest([]byte(`{"event":"$pageview","distinct_id":"ph-1","timestamp":"2026-01-01T00:00:00Z","properties":{"$current_url":"/x"}}`))
	if err != nil || len(evs) != 1 {
		t.Fatalf("decodeIngest(posthog) = %d events, %v; want 1, nil", len(evs), err)
	}
	if evs[0].DistinctID != "ph-1" {
		t.Errorf("the PostHog person (distinct_id) must survive, got %q", evs[0].DistinctID)
	}
	if evs[0].Type == "" {
		t.Error("the kind must be named — an unnamed kind is dropped whole by admitPublic")
	}

	// The PostHog batch envelope.
	evs, err = decodeIngest([]byte(`{"batch":[{"event":"$pageview","distinct_id":"ph-2","timestamp":"2026-01-01T00:00:00Z"}]}`))
	if err != nil || len(evs) != 1 || evs[0].DistinctID != "ph-2" {
		t.Fatalf("posthog batch: got %+v (%v)", evs, err)
	}

	// And the canonical wire is untouched by the new probe.
	evs, err = decodeIngest([]byte(`{"event":"$pageview","distinctId":"canon","time":"2026-01-01T00:00:00Z"}`))
	if err != nil || len(evs) != 1 || evs[0].DistinctID != "canon" {
		t.Fatalf("canonical wire must still decode canonically: %+v (%v)", evs, err)
	}
}
