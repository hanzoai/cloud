package analytics

import (
	"net/http"
	"testing"
)

// TestObsPlaneGetsFirstRefusalOnTheCanonicalDoor pins the unified event door:
// POST /v1/event is ONE door for every event kind. An authenticated body the
// o11y plane claims (an LLM-obs ingestion batch) is consumed by the claim with
// the SERVER-resolved org and never reaches the product warehouse; a body the
// claim declines walks the product wire exactly as before; and only the
// canonical door's FULL lane offers — the other doors and the anonymous lane
// The obs claim now crosses a PROCESS boundary over the plane socket, so it
// cannot be driven by swapping a package global in-process. What this package
// must still guarantee — and what a wrong answer would silently break — is that
// a claim which does NOT happen leaves the product wire fully intact: with no
// o11y peer reachable in a unit test, every body must still be decoded, written
// and receipted by analytics itself. The claim's own tenancy and shape rules are
// pinned next to the op, in apps/o11y.
func TestCanonicalDoorFallsThroughWhenObsPeerIsAbsent(t *testing.T) {
	w := fakeWarehouse(t)
	app := mountApp(t)

	// An LLM-obs-SHAPED body: with a peer it is claimed; without one it falls through
	// to the product wire, which has no writer for a trace envelope and lands NOTHING.
	//
	// THIS ASSERTION USED TO READ `want 200 via the product wire`, and 200 is what it
	// got — while storing zero facts. The trace was gone and the door said fine, which
	// is precisely how an o11y outage runs for months without anyone seeing it: the
	// only thing that changes is a number in a receipt nobody reads. What the door owes
	// a caller here is the truth — nothing landed, and the body is why (400) — not a
	// green check. The peer being ABSENT is a real operational state, and it now
	// surfaces at the door instead of being absorbed by it.
	obsBody := `{"batch":[{"id":"a","type":"trace-create","timestamp":"t","body":{}}]}`
	code, body := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme", obsBody)
	refused(t, "obs-shaped body with no peer", code, body, http.StatusBadRequest, "unroutable_events")
	if len(w.facts) != 0 {
		t.Fatalf("obs-shaped body landed %d facts on the product wire; it lands none, which is the "+
			"whole reason the receipt has to say so", len(w.facts))
	}

	// And an ordinary product event is untouched by the claim attempt — it is decoded,
	// WRITTEN and receipted 200 exactly as before. That is the guarantee this test
	// exists for: a claim that does not happen must not break the product wire.
	if code, got := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme",
		`{"event":"$pageview","distinctId":"d"}`); code != http.StatusOK {
		t.Fatalf("product event = %d (%s), want 200", code, got)
	}
	if got := w.sources(t); len(got) == 0 {
		t.Fatal("no peer must mean the product wire still WRITES; nothing reached the warehouse")
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
// /v1/insights/e: the door was removed, but until the canonical decode learned
// this wire's shape, a PostHog body landing on /v1/event decoded with an EMPTY
// person and an unnamed kind — which admitPublic drops whole, so the SDK saw a
// 200 that stored nothing. insights.hanzo.ai's /e, /batch and /capture all
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
