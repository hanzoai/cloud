package tenant

import (
	"fmt"
	"math"
	"testing"
)

func members(ids ...string) []Member {
	m := make([]Member, len(ids))
	for i, id := range ids {
		m[i] = Member{ID: id, Addr: id + ":9653"}
	}
	return m
}

// Determinism: the owner is independent of member slice order, and identical on
// every replica. This is the whole point — no coordinator, same answer everywhere.
func TestOwnerDeterministicRegardlessOfOrder(t *testing.T) {
	a := members("r1", "r2", "r3", "r4", "r5")
	b := members("r5", "r3", "r1", "r4", "r2") // same set, shuffled
	for _, tenant := range []string{"hanzo/z", "acme/dave", "org-123/proj-9", ""} {
		oa, oka := Owner(tenant, a)
		ob, okb := Owner(tenant, b)
		if !oka || !okb {
			t.Fatalf("tenant %q: expected an owner", tenant)
		}
		if oa.ID != ob.ID {
			t.Fatalf("tenant %q: owner depends on order (%s vs %s)", tenant, oa.ID, ob.ID)
		}
	}
}

func TestOwnerEmptyMembership(t *testing.T) {
	if _, ok := Owner("hanzo/z", nil); ok {
		t.Fatal("empty membership must return ok=false")
	}
	if IsOwner("hanzo/z", "r1", nil) {
		t.Fatal("IsOwner must be false with no members")
	}
}

func TestIsOwnerExactlyOneOwner(t *testing.T) {
	ms := members("r1", "r2", "r3")
	for _, tenant := range []string{"a", "b", "c", "hanzo/z", "x/y/z"} {
		owners := 0
		for _, m := range ms {
			if IsOwner(tenant, m.ID, ms) {
				owners++
			}
		}
		if owners != 1 {
			t.Fatalf("tenant %q: %d owners, want exactly 1", tenant, owners)
		}
	}
}

// Even distribution: ownership should spread ~uniformly over replicas, so no
// single replica is a write hot-spot. Allow generous slack (±35%) — this guards
// against a broken hash, not a statistics exam.
func TestOwnershipDistributionEven(t *testing.T) {
	ms := members("r1", "r2", "r3", "r4", "r5")
	const n = 50000
	count := map[string]int{}
	for i := 0; i < n; i++ {
		o, _ := Owner(fmt.Sprintf("tenant-%d", i), ms)
		count[o.ID]++
	}
	expect := float64(n) / float64(len(ms))
	for id, c := range count {
		dev := math.Abs(float64(c)-expect) / expect
		if dev > 0.35 {
			t.Errorf("replica %s owns %d (expected ~%.0f, dev %.0f%%) — skewed", id, c, expect, dev*100)
		}
	}
	if len(count) != len(ms) {
		t.Errorf("only %d/%d replicas own any tenant", len(count), len(ms))
	}
}

// Minimal reshuffle (the HRW property): removing one replica must move ONLY the
// tenants it owned; every other tenant keeps its owner. This is what makes
// scaling / rolling deploys cheap — a departing replica's ~1/N tenants migrate,
// the other (N-1)/N do not.
func TestMinimalReshuffleOnMemberLoss(t *testing.T) {
	before := members("r1", "r2", "r3", "r4")
	after := members("r1", "r2", "r3") // r4 left
	const n = 30000
	moved, ownedByR4 := 0, 0
	for i := 0; i < n; i++ {
		tenant := fmt.Sprintf("t-%d", i)
		ob, _ := Owner(tenant, before)
		oa, _ := Owner(tenant, after)
		if ob.ID == "r4" {
			ownedByR4++
		}
		if ob.ID != oa.ID {
			moved++
			// A tenant may only move if its previous owner was the departed r4.
			if ob.ID != "r4" {
				t.Fatalf("tenant %q moved from live replica %s→%s (should be stable)", tenant, ob.ID, oa.ID)
			}
		}
	}
	if moved != ownedByR4 {
		t.Fatalf("moved %d tenants but r4 owned %d — extra churn", moved, ownedByR4)
	}
	// Sanity: r4 owned roughly its 1/4 share.
	if ownedByR4 < n/4-n/10 || ownedByR4 > n/4+n/10 {
		t.Errorf("r4 owned %d (~1/4 = %d expected)", ownedByR4, n/4)
	}
}

// Replicas returns the owner first, then ordered failover successors, and the
// head must equal Owner().
func TestReplicasOrderedFailover(t *testing.T) {
	ms := members("r1", "r2", "r3", "r4", "r5")
	tenant := "hanzo/z"
	rs := Replicas(tenant, ms, 3)
	if len(rs) != 3 {
		t.Fatalf("want 3 replicas, got %d", len(rs))
	}
	o, _ := Owner(tenant, ms)
	if rs[0].ID != o.ID {
		t.Fatalf("Replicas[0]=%s must equal Owner=%s", rs[0].ID, o.ID)
	}
	// After the owner leaves, the old #2 becomes the new owner.
	remaining := make([]Member, 0, len(ms)-1)
	for _, m := range ms {
		if m.ID != o.ID {
			remaining = append(remaining, m)
		}
	}
	newOwner, _ := Owner(tenant, remaining)
	if newOwner.ID != rs[1].ID {
		t.Fatalf("failover: new owner %s != predicted successor %s", newOwner.ID, rs[1].ID)
	}
}
