package ossattr

import (
	"testing"
)

func pkgs() []Package {
	return []Package{
		{PURL: "pkg:golang/github.com/gin-gonic/gin@v1.12.0", Name: "gin", Ecosystem: "golang", Version: "v1.12.0", Scope: ScopeDirect},
		{PURL: "pkg:golang/github.com/google/uuid@v1.6.0", Name: "uuid", Ecosystem: "golang", Version: "v1.6.0", Scope: ScopeDirect},
		{PURL: "pkg:golang/golang.org/x/sys@v0.20.0", Name: "x/sys", Ecosystem: "golang", Version: "v0.20.0", Scope: ScopeTransitive},
		{PURL: "pkg:golang/github.com/bytedance/sonic@v1.11.0", Name: "sonic", Ecosystem: "golang", Version: "v1.11.0", Scope: ScopeTransitive},
	}
}

// The headline promise: the pool never exceeds 25% of spend, even if a policy
// asks for more.
func TestCapEnforced(t *testing.T) {
	cases := []struct {
		name     string
		frac     float64
		wantFrac float64
	}{
		{"default 25%", 0.25, 0.25},
		{"over-cap clamped", 0.90, 0.25},
		{"exactly cap", MaxPoolFraction, 0.25},
		{"ramp 10%", 0.10, 0.10},
		{"zero", 0, 0},
		{"negative clamped to 0", -0.5, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := DefaultPolicy()
			p.PoolFraction = tc.frac
			r := Attribute(1_000_000, pkgs(), p) // $10,000.00 spend
			if r.PoolFraction != tc.wantFrac {
				t.Fatalf("pool fraction = %v, want %v", r.PoolFraction, tc.wantFrac)
			}
			wantPool := int64(float64(1_000_000) * tc.wantFrac)
			if r.PoolCents != wantPool {
				t.Fatalf("pool cents = %d, want %d", r.PoolCents, wantPool)
			}
			if r.PoolCents > 250_000 {
				t.Fatalf("pool %d exceeds 25%% of spend (250000) — CAP VIOLATED", r.PoolCents)
			}
		})
	}
}

// Conservation: the sum of allocated cents + unallocated must equal the pool
// EXACTLY, for many spend values (largest-remainder must not leak a cent).
func TestCentConservation(t *testing.T) {
	p := DefaultPolicy()
	for spend := int64(1); spend <= 5000; spend++ {
		r := Attribute(spend, pkgs(), p)
		var sum int64
		for _, a := range r.Allocations {
			sum += a.Cents
			if a.Cents < 0 {
				t.Fatalf("spend=%d: negative allocation %d for %s", spend, a.Cents, a.PURL)
			}
		}
		if sum+r.UnallocatedCents != r.PoolCents {
			t.Fatalf("spend=%d: allocated %d + unallocated %d != pool %d",
				spend, sum, r.UnallocatedCents, r.PoolCents)
		}
	}
}

// Weighting: direct deps earn 4x a transitive dep by default (1.0 vs 0.25).
func TestDirectVsTransitiveWeighting(t *testing.T) {
	p := DefaultPolicy() // direct 1.0, transitive 0.25
	// 2 direct + 2 transitive => total weight = 2*1 + 2*0.25 = 2.5
	r := Attribute(2_500_000, pkgs(), p) // pool = 625000 cents
	if r.TotalWeight != 2.5 {
		t.Fatalf("total weight = %v, want 2.5", r.TotalWeight)
	}
	byPURL := map[string]Allocation{}
	for _, a := range r.Allocations {
		byPURL[a.PURL] = a
	}
	direct := byPURL["pkg:golang/github.com/gin-gonic/gin@v1.12.0"]
	transitive := byPURL["pkg:golang/golang.org/x/sys@v0.20.0"]
	// direct share = 1/2.5 = 0.4 of pool = 250000; transitive = 0.25/2.5 = 0.1 = 62500
	if direct.Cents != 250_000 {
		t.Fatalf("direct gin cents = %d, want 250000", direct.Cents)
	}
	if transitive.Cents != 62_500 {
		t.Fatalf("transitive x/sys cents = %d, want 62500", transitive.Cents)
	}
	if direct.Cents != 4*transitive.Cents {
		t.Fatalf("direct (%d) should be 4x transitive (%d)", direct.Cents, transitive.Cents)
	}
}

// Criticality multiplier scales a package's weight.
func TestCriticalityMultiplier(t *testing.T) {
	p := DefaultPolicy()
	ps := []Package{
		{PURL: "pkg:golang/a@v1", Name: "a", Scope: ScopeDirect, Criticality: 1.0},
		{PURL: "pkg:golang/b@v1", Name: "b", Scope: ScopeDirect, Criticality: 3.0},
	}
	// weights: a=1.0, b=3.0, total=4.0; pool over 400 cents => a=100, b=300.
	r := Attribute(1600, ps, p) // pool = 400
	byPURL := map[string]int64{}
	for _, a := range r.Allocations {
		byPURL[a.PURL] = a.Cents
	}
	if byPURL["pkg:golang/a@v1"] != 100 || byPURL["pkg:golang/b@v1"] != 300 {
		t.Fatalf("criticality split wrong: a=%d b=%d (want 100/300)",
			byPURL["pkg:golang/a@v1"], byPURL["pkg:golang/b@v1"])
	}
}

// De-dup: the same PURL across multiple SBOMs is paid once, keeping the
// highest weight (e.g. promoted from transitive to direct).
func TestDedupByPURL(t *testing.T) {
	p := DefaultPolicy()
	ps := []Package{
		{PURL: "pkg:npm/react@18.3.1", Name: "react", Scope: ScopeTransitive}, // 0.25
		{PURL: "pkg:npm/react@18.3.1", Name: "react", Scope: ScopeDirect},     // 1.0 wins
	}
	r := Attribute(1000, ps, p)
	if len(r.Allocations) != 1 {
		t.Fatalf("expected 1 deduped allocation, got %d", len(r.Allocations))
	}
	if r.Allocations[0].Scope != ScopeDirect {
		t.Fatalf("dedup should keep highest-weight (direct), got %s", r.Allocations[0].Scope)
	}
	if r.Allocations[0].Cents != r.PoolCents {
		t.Fatalf("single package should get whole pool: got %d want %d",
			r.Allocations[0].Cents, r.PoolCents)
	}
}

// No packages, or zero weight => whole pool is held (Unallocated), nothing lost.
func TestEmptyHoldsPool(t *testing.T) {
	p := DefaultPolicy()
	r := Attribute(1000, nil, p)
	if r.PoolCents != 250 || r.UnallocatedCents != 250 || len(r.Allocations) != 0 {
		t.Fatalf("empty: pool=%d unalloc=%d allocs=%d (want 250/250/0)",
			r.PoolCents, r.UnallocatedCents, len(r.Allocations))
	}

	pZeroWeight := Policy{PoolFraction: 0.25, DirectWeight: 0, TransitiveWeight: 0}
	r2 := Attribute(1000, pkgs(), pZeroWeight)
	if r2.UnallocatedCents != r2.PoolCents {
		t.Fatalf("zero-weight: whole pool should be held, got unalloc=%d pool=%d",
			r2.UnallocatedCents, r2.PoolCents)
	}
}

// Determinism: identical inputs => byte-identical allocations and order.
func TestDeterministicAndSorted(t *testing.T) {
	p := DefaultPolicy()
	r1 := Attribute(123_457, pkgs(), p)
	r2 := Attribute(123_457, pkgs(), p)
	if len(r1.Allocations) != len(r2.Allocations) {
		t.Fatalf("nondeterministic length")
	}
	for i := range r1.Allocations {
		if r1.Allocations[i] != r2.Allocations[i] {
			t.Fatalf("nondeterministic at %d: %+v vs %+v", i, r1.Allocations[i], r2.Allocations[i])
		}
		if i > 0 && r1.Allocations[i-1].PURL > r1.Allocations[i].PURL {
			t.Fatalf("allocations not sorted by PURL at %d", i)
		}
	}
}

// Zero / sub-cent spend behaves: spend so small the pool floors to 0.
func TestTinySpend(t *testing.T) {
	p := DefaultPolicy()
	r := Attribute(3, pkgs(), p) // 3 * 0.25 = 0.75 -> floor 0
	if r.PoolCents != 0 || len(r.Allocations) != 0 {
		t.Fatalf("tiny spend: pool=%d allocs=%d (want 0/0)", r.PoolCents, len(r.Allocations))
	}
	r2 := Attribute(0, pkgs(), p)
	if r2.PoolCents != 0 {
		t.Fatalf("zero spend pool=%d want 0", r2.PoolCents)
	}
}

// AccrualID is stable and varies with inputs.
func TestAccrualID(t *testing.T) {
	a := AccrualID("hanzo", "txn1", "pkg:golang/x@v1")
	b := AccrualID("hanzo", "txn1", "pkg:golang/x@v1")
	if a != b {
		t.Fatalf("AccrualID not stable: %s vs %s", a, b)
	}
	c := AccrualID("hanzo", "txn2", "pkg:golang/x@v1")
	if a == c {
		t.Fatalf("AccrualID should vary by txn")
	}
	if len(a) < 10 {
		t.Fatalf("AccrualID too short: %s", a)
	}
}

// Large-spend exactness: big.Int pool math must not drift.
func TestLargeSpendExact(t *testing.T) {
	p := DefaultPolicy()
	spend := int64(1_000_000_000_000) // $10,000,000,000.00
	r := Attribute(spend, pkgs(), p)
	if r.PoolCents != 250_000_000_000 {
		t.Fatalf("large pool = %d, want 250000000000", r.PoolCents)
	}
	var sum int64
	for _, a := range r.Allocations {
		sum += a.Cents
	}
	if sum != r.PoolCents {
		t.Fatalf("large spend conservation: sum %d != pool %d", sum, r.PoolCents)
	}
}
