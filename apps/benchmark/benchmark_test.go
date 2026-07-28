package benchmark

import "testing"

// Proves the arena serves REAL data: the leaderboard aggregates the append-only store
// coverage-aware, layers published claims, and computes the published−measured gap;
// compare is the paired common-set gate. Fixture = the real registry attempts.
func loadFixture(t *testing.T) []attempt {
	t.Helper()
	a := loadAttempts("testdata/attempts")
	if len(a) < 500 {
		t.Fatalf("fixture too small (%d) — did testdata/attempts sync?", len(a))
	}
	return a
}

func TestLeaderboardServesRealMeasuredVsPublished(t *testing.T) {
	rows := computeLeaderboard(loadFixture(t), "gpqa_diamond")
	if len(rows) == 0 {
		t.Fatal("empty leaderboard")
	}
	byModel := map[string]LeaderRow{}
	for _, r := range rows {
		byModel[r.Model] = r
	}
	// grok-4.5 measured on full 198
	if r, ok := byModel["grok-4.5"]; !ok || r.Measured == nil || r.N != 198 {
		t.Fatalf("grok-4.5 row wrong: %+v", r)
	}
	// fable-5: the published∥measured GAP is the whole point (claim 94.6 vs measured ~81)
	if r, ok := byModel["fable-5"]; ok && r.Measured != nil && r.Published != nil {
		if r.Gap == nil || *r.Gap < 5 {
			t.Fatalf("fable-5 gap should expose the inflated claim: %+v (gap %v)", r, r.Gap)
		}
		t.Logf("fable-5: measured %.1f vs published %.1f → gap +%.1f (claim runs hot)", *r.Measured, *r.Published, *r.Gap)
	}
	// coverage-aware: rows sorted by measured desc, top row has a real score
	if rows[0].Measured == nil {
		t.Fatal("top row has no measured score")
	}
	t.Logf("top measured arm: %s %.1f%% (n=%d)", rows[0].Model, *rows[0].Measured, rows[0].N)
}

func TestCompareIsPairedCommonSet(t *testing.T) {
	cmp := computeCompare(loadFixture(t), "gpqa_diamond", "grok-4.5", "gpt-5.6-sol")
	n := cmp["n_common"].(int)
	if n < 190 {
		t.Fatalf("grok vs gpt-5.6-sol common set too small: %d", n)
	}
	t.Logf("grok-4.5 vs gpt-5.6-sol: n=%d net=%v mcnemar_p=%v", n, cmp["net_a_minus_b"], cmp["mcnemar_p"])
}

func TestMcnemarExact(t *testing.T) {
	if p := mcnemarExact(0, 0); p != 1.0 {
		t.Fatalf("no discordant pairs must be p=1, got %v", p)
	}
	if p := mcnemarExact(10, 0); p >= 0.05 {
		t.Fatalf("10 vs 0 discordant should be significant, got %v", p)
	}
}
