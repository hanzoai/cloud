package org

import (
	"fmt"
	"math"
	"testing"
)

func mems(ids ...string) []Member {
	m := make([]Member, len(ids))
	for i, id := range ids {
		m[i] = Member{ID: id, Addr: id + ":9653"}
	}
	return m
}

// Determinism: the owner is independent of member slice order and identical on
// every replica — the whole point (no coordinator, same answer everywhere).
func TestOwnerDeterministicRegardlessOfOrder(t *testing.T) {
	a := mems("r1", "r2", "r3", "r4", "r5")
	b := mems("r5", "r3", "r1", "r4", "r2") // same set, shuffled
	for _, org := range []string{"hanzo", "acme", "org-123", ""} {
		oa, oka := Owner(org, a)
		ob, okb := Owner(org, b)
		if !oka || !okb {
			t.Fatalf("org %q: expected an owner", org)
		}
		if oa.ID != ob.ID {
			t.Fatalf("org %q: owner depends on order (%s vs %s)", org, oa.ID, ob.ID)
		}
	}
}

func TestOwnerEmptyMembership(t *testing.T) {
	if _, ok := Owner("hanzo", nil); ok {
		t.Fatal("empty membership must return ok=false")
	}
	if IsOwner("hanzo", "r1", nil) {
		t.Fatal("IsOwner must be false with no members")
	}
}

func TestIsOwnerExactlyOneOwner(t *testing.T) {
	ms := mems("r1", "r2", "r3")
	for _, org := range []string{"a", "b", "c", "hanzo", "acme"} {
		owners := 0
		for _, m := range ms {
			if IsOwner(org, m.ID, ms) {
				owners++
			}
		}
		if owners != 1 {
			t.Fatalf("org %q: %d owners, want exactly 1", org, owners)
		}
	}
}

// Even distribution: ownership spreads ~uniformly so no replica is a write
// hot-spot. Generous slack (±35%) — guards a broken hash, not a stats exam.
func TestOwnershipDistributionEven(t *testing.T) {
	ms := mems("r1", "r2", "r3", "r4", "r5")
	const n = 50000
	count := map[string]int{}
	for i := 0; i < n; i++ {
		o, _ := Owner(fmt.Sprintf("org-%d", i), ms)
		count[o.ID]++
	}
	expect := float64(n) / float64(len(ms))
	for id, c := range count {
		if dev := math.Abs(float64(c)-expect) / expect; dev > 0.35 {
			t.Errorf("replica %s owns %d (expected ~%.0f, dev %.0f%%) — skewed", id, c, expect, dev*100)
		}
	}
	if len(count) != len(ms) {
		t.Errorf("only %d/%d replicas own any org", len(count), len(ms))
	}
}

// Minimal reshuffle (the HRW property): removing one replica moves ONLY the orgs
// it owned; every other org keeps its owner. This is what makes scaling / rolling
// deploys cheap — a departing replica's ~1/N orgs migrate, the other (N-1)/N do not.
func TestMinimalReshuffleOnMemberLoss(t *testing.T) {
	before := mems("r1", "r2", "r3", "r4")
	after := mems("r1", "r2", "r3") // r4 left
	const n = 30000
	moved, ownedByR4 := 0, 0
	for i := 0; i < n; i++ {
		org := fmt.Sprintf("o-%d", i)
		ob, _ := Owner(org, before)
		oa, _ := Owner(org, after)
		if ob.ID == "r4" {
			ownedByR4++
		}
		if ob.ID != oa.ID {
			moved++
			if ob.ID != "r4" {
				t.Fatalf("org %q moved from live replica %s→%s (should be stable)", org, ob.ID, oa.ID)
			}
		}
	}
	if moved != ownedByR4 {
		t.Fatalf("moved %d orgs but r4 owned %d — extra churn", moved, ownedByR4)
	}
	if ownedByR4 < n/4-n/10 || ownedByR4 > n/4+n/10 {
		t.Errorf("r4 owned %d (~1/4 = %d expected)", ownedByR4, n/4)
	}
}

// Replicas returns the owner first, then ordered failover successors; the head
// equals Owner(), and after the owner leaves, the old #2 becomes the new owner.
func TestReplicasOrderedFailover(t *testing.T) {
	ms := mems("r1", "r2", "r3", "r4", "r5")
	const org = "hanzo"
	rs := Replicas(org, ms, 3)
	if len(rs) != 3 {
		t.Fatalf("want 3 replicas, got %d", len(rs))
	}
	o, _ := Owner(org, ms)
	if rs[0].ID != o.ID {
		t.Fatalf("Replicas[0]=%s must equal Owner=%s", rs[0].ID, o.ID)
	}
	remaining := make([]Member, 0, len(ms)-1)
	for _, m := range ms {
		if m.ID != o.ID {
			remaining = append(remaining, m)
		}
	}
	newOwner, _ := Owner(org, remaining)
	if newOwner.ID != rs[1].ID {
		t.Fatalf("failover: new owner %s != predicted successor %s", newOwner.ID, rs[1].ID)
	}
}
