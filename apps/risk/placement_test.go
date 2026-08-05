package risk

// placement_test.go — THE RINGS ONLY MOVE FORWARD, IN THE WINDOW THE RULES READ.
//
// velocity folds an event older than a window's span to that window's LEADING EDGE
// and reports it as displaced. Placement was bounded against the WIDEST span the
// rings keep, which is the one span nothing reaches — so a backdated event cleared it
// and was then counted as having just happened in the NARROWEST one, which is the
// window the pace bounds are read over.

import (
	"testing"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/velocity"
)

// TestRings_ABackdatedEventIsNotCountedAsNow.
//
// velocity folds an event older than a window's span to that window's LEADING EDGE
// and reports it as displaced — correct for a compliance aggregate that must not
// drop a record, and a detector evasion in reverse for a live rule: the event is
// counted as having just happened. [placeable] is the guard, and it was measured
// against [ringWindow], the WIDEST span the rings keep. An event ninety minutes old
// clears thirty days comfortably, so it was admitted — and then folded to now in the
// one-hour ring, which is the ring the pace bounds are read over. A caller could
// fill this hour's burst count with last month's history.
//
// THE DISPLACEMENT IS ASSERTED FROM THE ENGINE'S OWN COUNTER, not inferred from the
// sums. [velocity.Store.Late] counts exactly the writes that were folded to the
// leading edge, so "no write was displaced" is a measurement of the mechanism rather
// than of its consequence.
//
// Mutation proof: restore [ringWindow] in [placeable] and this fails twice — the
// late counter advances and the burst window counts two events where one happened.
func TestRings_ABackdatedEventIsNotCountedAsNow(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)

	clock := time.Now().UTC()
	p.now = func() time.Time { return clock }

	// One ordinary event now. It sets the leading edge.
	if n, err := p.learn(k, ob(t, "now", kindPayer, "u_back", 100, clock)); err != nil || n != 1 {
		t.Fatalf("learn the current event: %d, %v", n, err)
	}
	r, err := p.resident(k)
	if err != nil {
		t.Fatalf("resident: %v", err)
	}
	axis := velocity.Key{OrgID: string(k), Kind: anomaly.AxisAccount, Value: kindPayer + ":u_back"}
	before, _ := r.vel.pace(axis)
	displacedBefore := r.vel.vel.Late()

	// And one stamped further behind the edge than the window the rules read. It is
	// inside the record's own retention and inside the widest ring, so the ONLY thing
	// that can keep it out of the burst count is the narrow bound.
	learned, err := p.learn(k, ob(t, "backdated", kindPayer, "u_back", 100,
		clock.Add(-burstWindow-30*time.Minute)))
	if err != nil {
		t.Fatalf("learn the backdated event: %v", err)
	}
	// THE MODEL STILL LEARNS FROM IT. That is the stated trade and it has to hold:
	// the event is real history, it just does not get to say it happened now.
	if learned != 1 {
		t.Errorf("the backdated event was learned %d times, want 1 — refusing it from the "+
			"aggregates must not refuse it from the model", learned)
	}

	if got := r.vel.vel.Late(); got != displacedBefore {
		t.Errorf("velocity DISPLACED %d write(s) to the leading edge (was %d) — a backdated "+
			"event entered the rings and was counted as having just happened",
			got-displacedBefore, displacedBefore)
	}
	after, _ := r.vel.pace(axis)
	if after.Count != before.Count {
		t.Errorf("the %s window counts %d events where %d happened in it — history is being "+
			"read as now, which is the burst bound filled from the past",
			after.Span, after.Count, before.Count)
	}
	if after.Sum != before.Sum {
		t.Errorf("the %s window accrued %.2f where %.2f moved in it — the accrual bound is "+
			"reachable with backdated value", after.Span, after.Sum, before.Sum)
	}
}
