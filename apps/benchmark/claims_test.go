package benchmark

import (
	"testing"
	"time"
)

// A stored correction must outrank the seed, and an unattributed claim must be
// refused — those two are the whole contract of a manageable claims plane.
func TestStoredClaimOutranksTheSeedAndSourceIsRequired(t *testing.T) {
	dir := t.TempDir()
	st := newClaimStore(dir)

	seed := claimsFor(st, "gpqa_diamond")
	before, ok := seed["gpt-5.5"]
	if !ok {
		t.Fatal("seed lost gpt-5.5")
	}

	if err := st.Put(storedClaim{
		publishedClaim: publishedClaim{
			Benchmark: "gpqa_diamond", Provider: "openai", Model: "gpt-5.5",
			Score: 91.1, Protocol: "provider-reported", Source: "restated 2026-08-16",
		},
		At: time.Now().UTC().Add(time.Second),
	}); err != nil {
		t.Fatalf("put: %v", err)
	}

	after := claimsFor(st, "gpqa_diamond")["gpt-5.5"]
	if after.Score == before.Score {
		t.Fatalf("stored claim did not win: still %v", after.Score)
	}
	if after.Score != 91.1 || after.Source != "restated 2026-08-16" {
		t.Fatalf("effective claim = %+v", after)
	}
	// And the superseded row is still on disk — a correction is evidence.
	if n := len(st.Claims("gpqa_diamond")); n != 1 {
		t.Fatalf("stored rows = %d", n)
	}
	t.Logf("seed %.1f → stored %.1f, both retained", before.Score, after.Score)
}
