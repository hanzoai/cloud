package code

import "testing"

func ids(n ...int64) []chunkRow {
	out := make([]chunkRow, len(n))
	for i, id := range n {
		out[i] = chunkRow{ID: id}
	}
	return out
}

// A chunk that several tiers agree on must beat any single tier's top hit —
// the core reciprocal-rank-fusion property that makes hybrid retrieval SOTA.
func TestRRFConsensusWins(t *testing.T) {
	text := ids(1, 2, 3)     // id3 ranks 3rd here
	symbol := ids(3, 4)      // id3 ranks 1st
	semantic := ids(3, 5, 6) // id3 ranks 1st

	fused := rrf([][]chunkRow{text, symbol, semantic}, 10)
	if len(fused) == 0 || fused[0].ID != 3 {
		t.Fatalf("expected id3 (three-tier consensus) first, got %+v", fused)
	}
	// id1 is only a single tier's rank-1; it must not outrank the consensus.
	if fused[0].Score <= 1.0/(rrfK+1) {
		t.Errorf("consensus score %v not above a single rank-1 term", fused[0].Score)
	}
}

func TestRRFDeduplicatesAndBounds(t *testing.T) {
	a := ids(1, 2, 3, 4, 5)
	b := ids(3, 4, 5, 6, 7)
	fused := rrf([][]chunkRow{a, b}, 3)
	if len(fused) != 3 {
		t.Fatalf("limit not applied: got %d want 3", len(fused))
	}
	seen := map[int64]bool{}
	for _, r := range fused {
		if seen[r.ID] {
			t.Fatalf("duplicate id %d in fused result", r.ID)
		}
		seen[r.ID] = true
	}
}

func TestRRFEmpty(t *testing.T) {
	if got := rrf(nil, 5); len(got) != 0 {
		t.Fatalf("rrf(nil)=%v want empty", got)
	}
}
