package risk

// rollup_product_test.go — "A SCORE RECORDS NOTHING" MUST HOLD MORE THAN ONE HOP.
//
// Every decision is stated onto the shared event plane (emit.go), and the person
// rollup folded every signal='act' fact with no product filter — so the decisions came
// back as ACTIVITY and the model learned from its own output. A subject screened often
// enough became unusual for having been screened.

import (
	"context"
	"testing"
	"time"
)

// TestRollup_ThisAppsOwnDecisionsAreNotFoldedBackIntoTheModel.
//
// "A score records nothing" is the invariant that keeps this plane from measuring
// itself, and it held for exactly one hop. Every decision is ALSO stated on the
// shared event plane (emit.go) — one `risk_decided` row per decide, under product
// 'risk', with the decided subject as its distinct id — and the person rollup folded
// every signal='act' fact with no product filter at all. So the decisions came back
// as ACTIVITY: a subject screened often enough became unusual for having been
// screened, and the model learned from its own output one hop later.
//
// The rollup that folds a plane now names the product it will not fold.
//
// Mutation proof: remove [notOurOwn] from the person rollup and the decision row
// below becomes a surface subject, which is exactly what this fails on.
func TestRollup_ThisAppsOwnDecisionsAreNotFoldedBackIntoTheModel(t *testing.T) {
	probe.reset(true)
	p := newTestPlane(t)
	holdFolds(t, p)
	k := key(t, brandA, orgA)
	now := time.Now().UTC()
	at := now.Add(-2 * time.Hour)

	// The organisation's OWN product events, which must fold.
	probe.emit(orgA, emitted{Plane: "person", Subject: "u_real", At: at})
	// And THIS APP's own decisions about a subject, stated onto the same table under
	// its own product, which must not.
	for i := range 6 {
		probe.emit(orgA, emitted{
			Plane:   "person",
			Subject: digest(k, kindPayer, "u_screened"),
			At:      at.Add(time.Duration(i) * time.Minute),
			Product: surface,
		})
	}

	if _, err := p.roll(context.Background(), k); err != nil {
		t.Fatalf("roll: %v", err)
	}
	rs, err := rows(context.Background(), k, query{start: now.Add(-warmWindow), end: now})
	if err != nil {
		t.Fatalf("read the feature surface: %v", err)
	}
	if len(rs) == 0 {
		t.Fatal("nothing folded at all — this test proves nothing unless the ordinary event folds")
	}
	decided := digest(k, kindPayer, "u_screened")
	var real, own int
	for _, r := range rs {
		switch r.Subject {
		case "u_real":
			real++
		case decided:
			own++
		}
	}
	if real == 0 {
		t.Error("the organisation's own product event did not fold — the exclusion is too wide " +
			"and it has taken real history with it")
	}
	if own != 0 {
		t.Errorf("%d of this app's own risk decisions became a surface subject — the model now "+
			"learns from its own output, and a subject screened often enough is unusual for "+
			"having been screened", own)
	}
}
