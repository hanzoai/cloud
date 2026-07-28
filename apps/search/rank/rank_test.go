package rank

import "testing"

// TestRRFReinforcesAgreement is the reason hybrid search exists: a document both
// sources found must outrank a document only one source found, even when the
// single-source document sits at the very top of its list.
func TestRRFReinforcesAgreement(t *testing.T) {
	out := RRF([]List{
		{Source: "index", Keys: []string{"solo-text", "agreed"}},
		{Source: "vector", Keys: []string{"solo-vec", "agreed"}},
	}, 10)

	if len(out) != 3 {
		t.Fatalf("want 3 fused keys, got %d: %+v", len(out), out)
	}
	if out[0].Key != "agreed" {
		t.Fatalf("a doc both sources ranked #2 must beat docs one source ranked #1; got %q first: %+v", out[0].Key, out)
	}
	// 2/(60+2) vs 1/(60+1)
	if want := 2.0 / 62.0; out[0].Score != want {
		t.Fatalf("fused score = %v, want %v", out[0].Score, want)
	}
	if len(out[0].Origins) != 2 {
		t.Fatalf("agreed doc must carry BOTH origins, got %+v", out[0].Origins)
	}
}

// TestRRFProvenance proves every fused row can explain itself: which source, at
// which rank, with that source's native score. Without this a hybrid ranking is
// undebuggable.
func TestRRFProvenance(t *testing.T) {
	out := RRF([]List{
		{Source: "vector", Keys: []string{"a", "b"}, Scores: []float64{0.91, 0.42}},
	}, 10)

	if len(out) != 2 {
		t.Fatalf("want 2 rows, got %d", len(out))
	}
	o := out[0].Origins[0]
	if o.Source != "vector" || o.Rank != 1 || o.Score != 0.91 {
		t.Fatalf("origin = %+v, want {vector 1 0.91}", o)
	}
	if o2 := out[1].Origins[0]; o2.Rank != 2 || o2.Score != 0.42 {
		t.Fatalf("second origin = %+v, want rank 2 score 0.42", o2)
	}
}

// TestRRFSurvivesMissingSource is the degradation invariant: when a source drops
// out, the survivor's ORDER is unchanged. A partial answer must still be a
// correctly ordered answer.
func TestRRFSurvivesMissingSource(t *testing.T) {
	both := RRF([]List{
		{Source: "index", Keys: []string{"x", "y", "z"}},
		{Source: "vector", Keys: []string{"q"}},
	}, 10)
	only := RRF([]List{
		{Source: "index", Keys: []string{"x", "y", "z"}},
	}, 10)

	order := func(f []Fused) string {
		s := ""
		for _, r := range f {
			if r.Key != "q" {
				s += r.Key
			}
		}
		return s
	}
	if order(both) != order(only) {
		t.Fatalf("losing a source reordered the survivor: %q vs %q", order(both), order(only))
	}
	if len(only) != 3 {
		t.Fatalf("single-source fusion must return all its keys, got %d", len(only))
	}
}

// TestRRFStableAndBounded pins tie-breaking and the limit, so paging an identical
// query twice cannot shuffle rows.
func TestRRFStableAndBounded(t *testing.T) {
	lists := []List{{Source: "index", Keys: []string{"a", "b", "c", "d"}}}
	first := RRF(lists, 2)
	second := RRF(lists, 2)

	if len(first) != 2 {
		t.Fatalf("limit ignored: got %d rows", len(first))
	}
	for i := range first {
		if first[i].Key != second[i].Key {
			t.Fatalf("unstable ordering at %d: %q vs %q", i, first[i].Key, second[i].Key)
		}
	}
	if first[0].Key != "a" || first[1].Key != "b" {
		t.Fatalf("rank order not preserved: %+v", first)
	}
}

// TestRRFEmpty proves no-input is an empty result, not a panic — the shape a
// fully-degraded query produces.
func TestRRFEmpty(t *testing.T) {
	if out := RRF(nil, 10); len(out) != 0 {
		t.Fatalf("want empty, got %+v", out)
	}
	if out := RRF([]List{{Source: "index"}}, 10); len(out) != 0 {
		t.Fatalf("want empty, got %+v", out)
	}
}
