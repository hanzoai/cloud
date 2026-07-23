package experiments

import (
	"math"
	"testing"
)

// TestTwoProportionZ_PinnedReference pins the significance math to an independently
// computed reference: control 100/1000 (10%) vs treatment 150/1000 (15%) yields the
// classic z = 3.3806, two-tailed p = 0.000723. The formula may never drift silently.
func TestTwoProportionZ_PinnedReference(t *testing.T) {
	z, p := twoProportionZ(100, 1000, 150, 1000)
	if math.Abs(z-3.3806) > 1e-3 {
		t.Fatalf("z drifted: got %.6f want ~3.3806", z)
	}
	if math.Abs(p-0.000723) > 5e-5 {
		t.Fatalf("p drifted: got %.6f want ~0.000723", p)
	}
	// Symmetry: swapping arms flips the sign of z, same |z| and p.
	z2, p2 := twoProportionZ(150, 1000, 100, 1000)
	if math.Abs(z2+z) > 1e-9 || math.Abs(p2-p) > 1e-9 {
		t.Fatalf("z-test not symmetric: (%.6f,%.6f) vs (%.6f,%.6f)", z, p, z2, p2)
	}
}

// TestTwoProportionZ_Degenerate proves the degenerate inputs fail SAFE: an empty arm
// or zero variance returns (0, 1) — never significant, never a divide-by-zero.
func TestTwoProportionZ_Degenerate(t *testing.T) {
	cases := [][4]int{
		{0, 0, 5, 100},   // empty control
		{5, 100, 0, 0},   // empty treatment
		{0, 100, 0, 100}, // no conversions anywhere -> zero variance
	}
	for _, c := range cases {
		z, p := twoProportionZ(c[0], c[1], c[2], c[3])
		if z != 0 || p != 1 {
			t.Fatalf("degenerate %v must be (0,1), got (%.4f,%.4f)", c, z, p)
		}
	}
}

// TestTwoProportionZ_NoDifference: identical rates -> z 0, p 1.
func TestTwoProportionZ_NoDifference(t *testing.T) {
	z, p := twoProportionZ(100, 1000, 100, 1000)
	if z != 0 || p != 1 {
		t.Fatalf("equal proportions must be (0,1), got (%.4f,%.4f)", z, p)
	}
}

func exp2() Experiment {
	return Experiment{
		ID:          "checkout",
		MetricEvent: "order_completed",
		Variants: []Variant{
			{Key: "control", Control: true, Weight: 50},
			{Key: "treatment", Weight: 50},
		},
	}
}

// TestComputeAnalysis_LiftSignificanceWinner exercises the full per-variant read: the
// treatment beats control with a large sample, so lift > 0, it is significant, and it
// is the winner. Control carries no lift/stats (it is its own baseline).
func TestComputeAnalysis_LiftSignificanceWinner(t *testing.T) {
	samples := map[string]*sample{
		"control":   {exposed: 1000, converted: 100}, // 10%
		"treatment": {exposed: 1000, converted: 200}, // 20%
	}
	a := computeAnalysis(exp2(), samples, 0.05)

	if a.Results[0].Variant != "control" || !a.Results[0].Control {
		t.Fatalf("control must sort first, got %+v", a.Results[0])
	}
	if a.Results[0].Lift != 0 || a.Results[0].PValue != 0 {
		t.Fatalf("control must carry no lift/stats: %+v", a.Results[0])
	}
	tr := a.Results[1]
	if tr.Variant != "treatment" {
		t.Fatalf("want treatment second, got %s", tr.Variant)
	}
	if tr.Rate != 0.2 {
		t.Fatalf("treatment rate = %v want 0.2", tr.Rate)
	}
	if math.Abs(tr.Lift-1.0) > 1e-9 { // (0.2-0.1)/0.1 = 1.0
		t.Fatalf("treatment lift = %v want 1.0", tr.Lift)
	}
	if !tr.Significant {
		t.Fatalf("treatment must be significant (p=%v)", tr.PValue)
	}
	if a.Winner != "treatment" {
		t.Fatalf("winner = %q want treatment", a.Winner)
	}
	if a.ExposedTotal != 2000 {
		t.Fatalf("exposedTotal = %d want 2000", a.ExposedTotal)
	}
}

// TestComputeAnalysis_InconclusiveNoWinner: a tiny, noisy difference is not
// significant, so there is NO winner (the primitive never promotes on noise).
func TestComputeAnalysis_InconclusiveNoWinner(t *testing.T) {
	samples := map[string]*sample{
		"control":   {exposed: 100, converted: 10}, // 10%
		"treatment": {exposed: 100, converted: 12}, // 12% — well within noise at n=100
	}
	a := computeAnalysis(exp2(), samples, 0.05)
	if a.Results[1].Significant {
		t.Fatalf("small n, small delta must be inconclusive, p=%v", a.Results[1].PValue)
	}
	if a.Winner != "" {
		t.Fatalf("no significant arm -> no winner, got %q", a.Winner)
	}
}

// TestComputeAnalysis_MissingArmIsZero: a variant with no samples still appears with
// Exposed 0, so the read is complete over the declared arms.
func TestComputeAnalysis_MissingArmIsZero(t *testing.T) {
	a := computeAnalysis(exp2(), map[string]*sample{"control": {exposed: 500, converted: 50}}, 0.05)
	if len(a.Results) != 2 {
		t.Fatalf("want a row per declared arm, got %d", len(a.Results))
	}
	if a.Results[1].Exposed != 0 || a.Results[1].Rate != 0 {
		t.Fatalf("absent arm must be zeroed, got %+v", a.Results[1])
	}
}

// TestFoldOutcomes_JoinsAssignmentAndSkips proves the join: each exposed subject is
// bucketed by its assignment; unexposed, unassigned (variant ""), and assign-error
// subjects are dropped, so the denominator is exactly "assigned AND exposed".
func TestFoldOutcomes_JoinsAssignmentAndSkips(t *testing.T) {
	outcomes := []MetricOutcome{
		{Subject: "a", Exposed: true, Converted: true},  // -> control, converts
		{Subject: "b", Exposed: true, Converted: false}, // -> control
		{Subject: "c", Exposed: true, Converted: true},  // -> treatment, converts
		{Subject: "d", Exposed: false, Converted: true}, // unexposed -> skip
		{Subject: "e", Exposed: true, Converted: true},  // unassigned "" -> skip
		{Subject: "f", Exposed: true, Converted: true},  // assign error -> skip
	}
	assign := func(subject string) (string, error) {
		switch subject {
		case "a", "b":
			return "control", nil
		case "c":
			return "treatment", nil
		case "e":
			return "", nil
		case "f":
			return "", errTest
		}
		return "", nil
	}
	got := foldOutcomes(outcomes, assign)
	if got["control"].exposed != 2 || got["control"].converted != 1 {
		t.Fatalf("control fold wrong: %+v", got["control"])
	}
	if got["treatment"].exposed != 1 || got["treatment"].converted != 1 {
		t.Fatalf("treatment fold wrong: %+v", got["treatment"])
	}
	if _, ok := got[""]; ok {
		t.Fatalf("empty-variant subjects must never bucket")
	}
}

var errTest = &testErr{}

type testErr struct{}

func (*testErr) Error() string { return "assign failed" }
