package benchmark

import (
	"math"
	"testing"
)

// The interval is the point of the whole thing: it says whether a difference at
// the top of the board is real.
func TestWilsonSaysTheTopOfTheBoardIsNotDistinguishable(t *testing.T) {
	// The enso tiers, as measured: 194/198 and 190/198.
	uLo, uHi := wilson(194, 198)
	pLo, pHi := wilson(190, 198)

	if uLo >= uHi || pLo >= pHi {
		t.Fatalf("degenerate intervals: %v-%v, %v-%v", uLo, uHi, pLo, pHi)
	}
	// 98.0 and 96.0 look like a clear two-point gap and their intervals overlap,
	// which is exactly what a bare leaderboard hides.
	if pHi <= uLo {
		t.Errorf("intervals do not overlap (%.1f-%.1f vs %.1f-%.1f) — expected them to", pLo, pHi, uLo, uHi)
	}
	// And the interval must never leave the possible range at the top, which is
	// the normal approximation's failure and the reason for Wilson.
	if hi := uHi; hi > 100 {
		t.Errorf("upper bound %.2f exceeds 100", hi)
	}
	t.Logf("194/198 → %.1f–%.1f   190/198 → %.1f–%.1f", uLo, uHi, pLo, pHi)
}

func TestWilsonEdges(t *testing.T) {
	if lo, hi := wilson(0, 0); lo != 0 || hi != 0 {
		t.Errorf("no trials = no interval, got %v-%v", lo, hi)
	}
	// A perfect score does not mean certainty; the interval must still have width.
	lo, hi := wilson(10, 10)
	if hi != 100 || lo >= 100 {
		t.Errorf("10/10 → %.2f-%.2f, want a lower bound below 100", lo, hi)
	}
	// Symmetry: k and n-k mirror around 50.
	aLo, aHi := wilson(3, 10)
	bLo, bHi := wilson(7, 10)
	if math.Abs((100-aHi)-bLo) > 1e-9 || math.Abs((100-aLo)-bHi) > 1e-9 {
		t.Errorf("not symmetric: %v-%v vs %v-%v", aLo, aHi, bLo, bHi)
	}
}
