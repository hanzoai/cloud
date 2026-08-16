package benchmark

import (
	"testing"
	"time"
)

// The contract of a manageable claims plane, in the order it matters:
// a second SOURCE is kept beside the first, a restatement by the SAME source
// replaces it, and nothing is ever lost from disk.
func TestASecondSourceIsKeptAndARestatementReplaces(t *testing.T) {
	st := newClaimStore(t.TempDir())

	seed := claimsFor(st, "gpqa_diamond")["gpt-5.5"]
	if len(seed) == 0 {
		t.Fatal("seed lost gpt-5.5")
	}

	// A different source is a second independent reading, not a correction.
	third := publishedClaim{
		Benchmark: "gpqa_diamond", Provider: "artificial-analysis", Model: "gpt-5.5",
		Score: 88.1, Protocol: "third-party-leaderboard", Source: "Artificial Analysis 2026-08",
	}
	if err := st.Put(storedClaim{publishedClaim: third, At: time.Now().UTC()}); err != nil {
		t.Fatalf("put: %v", err)
	}

	both := claimsFor(st, "gpqa_diamond")["gpt-5.5"]
	if len(both) != len(seed)+1 {
		t.Fatalf("claims = %d, want %d — a new source must not replace another", len(both), len(seed)+1)
	}

	// The column shows the provider's own claim, because that is the number the
	// arena reconciles against a measurement.
	sel, _ := selectClaim(both)
	if sel.Protocol != "provider-reported" {
		t.Errorf("selected %q, want the provider-reported claim", sel.Protocol)
	}
	// And the disagreement is visible rather than implied.
	if sp := claimSpread(both); sp == nil || *sp < 1 {
		t.Errorf("spread = %v, want the distance between the readings", sp)
	}

	// The SAME source restating is a correction: it replaces, and the superseded
	// row stays on disk because a restatement is itself evidence.
	restated := third
	restated.Score = 90.4
	if err := st.Put(storedClaim{publishedClaim: restated, At: time.Now().UTC().Add(time.Second)}); err != nil {
		t.Fatalf("put restated: %v", err)
	}
	after := claimsFor(st, "gpqa_diamond")["gpt-5.5"]
	if len(after) != len(both) {
		t.Fatalf("claims = %d after a restatement, want %d", len(after), len(both))
	}
	var got float64
	for _, c := range after {
		if c.Source == third.Source {
			got = c.Score
		}
	}
	if got != 90.4 {
		t.Errorf("restated claim = %v, want 90.4", got)
	}
	if n := len(st.Claims("gpqa_diamond")); n != 2 {
		t.Errorf("rows on disk = %d, want 2 — append-only keeps the superseded value", n)
	}
}
