package risk

// stale_test.go — A VELOCITY WINDOW MUST EXPIRE ON ITS OWN.
//
// The window is anchored to the last event the rings were TAUGHT, not to the clock,
// and the op that reads it records nothing — so nothing advances the anchor and
// nothing slides the window. A busy hour therefore froze its subject permanently.
// This is the pair of assertions that makes the reading a control: the burst is found
// while it is current, and it is gone once the clock has left it behind.

import (
	"testing"
	"time"

	"github.com/hanzoai/cloud"
)

// TestPace_AStaleWindowStopsFreezingTheSubject.
//
// The defect this pins is not a tuning error, it is a permanent state. The 1h ring
// sums the buckets in [lead−59, lead] where `lead` is the newest event the rings
// were TAUGHT — so a subject that did sixty funded things inside one hour last
// Tuesday reads sixty events in "the last hour" for as long as the residency lives.
// Nothing clears it: a decide records nothing by design, so the edge never advances
// and the window never slides. Every later payment by that organisation was frozen
// by a burst that had finished a week earlier.
//
// It asserts BOTH directions, because only the pair is a control: the burst is found
// while it is current, and it is gone once the clock has left the window behind — on
// the SAME aggregates, with nothing taught in between and nothing recorded.
//
// Mutation proof: delete the staleness test in [plane.prior] and the second half
// fails with action=restrict — the frozen-forever state, reproduced.
func TestPace_AStaleWindowStopsFreezingTheSubject(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	arm(t, p, k)

	// The plane's clock is a seam, so this drives time instead of waiting for it.
	clock := time.Now().UTC()
	p.now = func() time.Time { return clock }

	// A funded burst on one payer, inside the burst window: past the count bound and
	// accruing past the freeze, so only the conjunction can fire.
	at := clock.Add(-10 * time.Minute)
	batch := make([]observation, 0, burstEvents)
	for i := 0; i < burstEvents; i++ {
		batch = append(batch, ob(t, "stale_"+itoa(i), kindPayer, "u_burst", 200,
			at.Add(time.Duration(i)*time.Second)))
	}
	if _, err := p.learn(k, batch...); err != nil {
		t.Fatalf("learn: %v", err)
	}
	judged := ob(t, "judged", kindPayer, "u_burst", 200, clock)

	// WHILE IT IS CURRENT the burst is found. Without this the test below would pass
	// against a rule that never fires at all.
	seen, err := p.prior(k, judged)
	if err != nil {
		t.Fatalf("prior: %v", err)
	}
	if len(seen.Pace) == 0 {
		t.Fatal("the aggregates reported no window at all while the burst was current — " +
			"this test proves nothing unless the burst is findable first")
	}
	if got := onPace(seen); got.Action != cloud.ActionRestrict {
		t.Fatalf("a current funded burst was %q, want %q — the rule does not fire, so its "+
			"going quiet later proves nothing", got.Action, cloud.ActionRestrict)
	}

	// NOW ONLY THE CLOCK MOVES. Nothing is taught, nothing is recorded, and the rings
	// hold exactly the same buckets — which is the whole point: the reading has to
	// expire on its own, because there is no event that would expire it.
	span := seen.Pace[0].Span
	clock = clock.Add(span + time.Hour)

	after, err := p.prior(k, judged)
	if err != nil {
		t.Fatalf("prior after the window: %v", err)
	}
	if len(after.Pace) != 0 {
		t.Errorf("the aggregates still report %d window(s) %s after the last event they were "+
			"taught — a reading anchored to the last event rather than to the clock never "+
			"expires, and the subject is frozen for good", len(after.Pace), span+time.Hour)
	}
	if got := onPace(after); got.fired() {
		t.Errorf("a burst that finished %s ago still returns %q (%q) — nothing can clear it, "+
			"because the op that reads it records nothing", span+time.Hour, got.Action, got.Cause)
	}
}
