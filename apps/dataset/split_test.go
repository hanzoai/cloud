package dataset

// split_test.go tests the pure half directly. Everything here is a function of
// its arguments, so these are the assertions that hold whatever the store does —
// and they are the ones an auditor's question ("is this split reproducible?")
// actually turns on.

import (
	"math"
	"slices"
	"testing"
	"time"
)

var (
	t0   = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	edges = [2]time.Time{t0.Add(70 * time.Hour), t0.Add(85 * time.Hour)}
)

func at(h int) time.Time { return t0.Add(time.Duration(h) * time.Hour) }

func facts(spec ...[2]int) []fact {
	out := make([]fact, 0, len(spec))
	for _, s := range spec {
		out = append(out, fact{
			Kind:    kindPerson,
			Subject: string(rune('a' + s[0])),
			At:      at(s[1]),
			Point:   []float64{float64(s[1]), 1},
		})
	}
	return out
}

// TestAssignIsTheThreeWayTemporalCut, boundaries included: a cut is half-open, so
// an instant exactly ON a cut belongs to the later side and no row can fall in
// two splits or in none.
func TestAssignIsTheThreeWayTemporalCut(t *testing.T) {
	for _, tc := range []struct {
		hour int
		want uint8
	}{
		{0, train}, {69, train},
		{70, val}, {84, val},
		{85, test}, {1000, test},
	} {
		if got := assign(edges, at(tc.hour)); got != tc.want {
			t.Errorf("hour %d landed in %s, want %s", tc.hour, splitName(got), splitName(tc.want))
		}
	}
}

// TestCutIsDeterministic: the same facts in ANY order produce byte-identical
// rows. A caller cannot make two runs differ by asking in a different sequence,
// and neither can a store returning parts in a different order.
func TestCutIsDeterministic(t *testing.T) {
	in := facts([2]int{0, 10}, [2]int{1, 80}, [2]int{2, 90}, [2]int{0, 95}, [2]int{1, 5})
	forward := cut(in, edges)

	shuffled := []fact{in[3], in[0], in[4], in[2], in[1]}
	backward := cut(shuffled, edges)

	if len(forward) != len(backward) {
		t.Fatalf("%d rows vs %d", len(forward), len(backward))
	}
	for i := range forward {
		a, b := forward[i], backward[i]
		if a.ID != b.ID || a.Split != b.Split || a.Kind != b.Kind || a.Subject != b.Subject || !a.At.Equal(b.At) {
			t.Fatalf("row %d differs by input order:\n  %+v\n  %+v", i, a, b)
		}
		if !slices.Equal(a.Point, b.Point) {
			t.Fatalf("row %d's coordinates differ by input order: %v vs %v", i, a.Point, b.Point)
		}
	}
	s := spec{Name: "d", Dims: []string{"events", "ips"}, From: t0, To: at(100), Cuts: edges, Seed: "x", Rows: 10}
	if digest(s, 1, forward) != digest(s, 1, backward) {
		t.Fatal("the digest depends on the order the facts arrived in")
	}
}

// TestASubjectIsNeverSplit is R2. Subject `a` acts at hour 10 (train) and again
// at hour 95 (test); both rows must be train, because the subject was FIRST seen
// in train and moving it forward would put its training-window behaviour inside
// the test set.
func TestASubjectIsNeverSplit(t *testing.T) {
	rows := cut(facts([2]int{0, 10}, [2]int{0, 95}, [2]int{1, 80}, [2]int{1, 99}), edges)
	if n := coherent(rows); n != 0 {
		t.Fatalf("%d subjects landed in more than one split", n)
	}
	for _, r := range rows {
		switch r.Subject {
		case "a":
			if r.Split != train {
				t.Errorf("subject a was first seen in train and landed in %s", splitName(r.Split))
			}
		case "b":
			if r.Split != val {
				t.Errorf("subject b was first seen in val and landed in %s", splitName(r.Split))
			}
		}
	}
}

// TestTheSameSubjectStringInTwoKindsIsTwoSubjects. The identity is (kind,
// subject): a session id and an account id that happen to be the same string are
// not one entity, and folding them would move rows between splits for no reason.
func TestTheSameSubjectStringInTwoKindsIsTwoSubjects(t *testing.T) {
	in := []fact{
		{Kind: kindPerson, Subject: "x", At: at(10)},
		{Kind: kindSession, Subject: "x", At: at(90)},
	}
	rows := cut(in, edges)
	if rows[0].Split == rows[1].Split {
		t.Fatal("two kinds sharing a subject string were folded into one entity")
	}
	if coherent(rows) != 0 {
		t.Fatal("coherence is being computed over the subject alone")
	}
}

// TestRowIDsAreDerivedAndStable. An id is a function of the row, so two
// materialisations agree without coordinating — and two different rows do not
// collide.
func TestRowIDsAreDerivedAndStable(t *testing.T) {
	a := fact{Kind: kindPerson, Subject: "s", At: at(1)}
	if a.id() != a.id() {
		t.Fatal("an id is not a function of the row")
	}
	seen := map[string]bool{}
	for _, f := range []fact{
		{Kind: kindPerson, Subject: "s", At: at(1)},
		{Kind: kindPerson, Subject: "s", At: at(2)},
		{Kind: kindSession, Subject: "s", At: at(1)},
		{Kind: kindPerson, Subject: "s2", At: at(1)},
		// The separator matters: without it "a"+"bc" and "ab"+"c" would hash alike.
		{Kind: kindPerson, Subject: "a\x00b", At: at(1)},
		{Kind: kindPerson, Subject: "a", At: at(1)},
	} {
		if seen[f.id()] {
			t.Fatalf("two different facts share an id: %+v", f)
		}
		seen[f.id()] = true
	}
	// The coordinates are NOT part of the id: a row is identified by whose bucket
	// it is, and the digest is what covers what the bucket held.
	b := fact{Kind: kindPerson, Subject: "s", At: at(1), Point: []float64{9, 9}}
	if a.id() != b.id() {
		t.Fatal("the id depends on the coordinates, so a rollup that merged a late part would rename a row")
	}
}

// TestTheDigestCoversTheQuestionAndTheAnswer. Change anything — a coordinate, a
// split, the spec, the version — and the fingerprint moves.
func TestTheDigestCoversTheQuestionAndTheAnswer(t *testing.T) {
	s := spec{Name: "d", Kind: kindPerson, Dims: []string{"events", "ips"}, From: t0, To: at(100), Cuts: edges, Seed: "x", Rows: 10}
	rows := cut(facts([2]int{0, 10}, [2]int{1, 80}, [2]int{2, 90}), edges)
	base := digest(s, 1, rows)

	if again := digest(s, 1, rows); again != base {
		t.Fatal("the digest is not a function of its inputs")
	}
	if digest(s, 2, rows) == base {
		t.Fatal("two versions of one spec share a digest, so a citation cannot name a version")
	}

	other := s
	other.Seed = "y"
	if digest(other, 1, rows) == base {
		t.Fatal("the seed is not in the digest, so two memberships could claim one fingerprint")
	}
	other = s
	other.Dims = []string{"ips", "events"}
	if digest(other, 1, rows) == base {
		t.Fatal("the dim order is not in the digest, so a point vector could be reinterpreted")
	}

	moved := append([]row(nil), rows...)
	moved[0].Split = test
	if digest(s, 1, moved) == base {
		t.Fatal("the split assignment is not in the digest")
	}

	nudged := append([]row(nil), rows...)
	nudged[0].Point = []float64{math.Nextafter(rows[0].Point[0], 1e9)}
	if digest(s, 1, nudged) == base {
		t.Fatal("a one-ulp change in a coordinate does not move the digest")
	}
}

// TestShareFitsTheWindowToTheCap.
func TestShareFitsTheWindowToTheCap(t *testing.T) {
	for _, tc := range []struct{ rows, budget, want int }{
		{0, 100, shareDenominator},
		{50, 100, shareDenominator},
		{100, 100, shareDenominator},
		{200, 100, 500},
		{1000, 1, 1},
		{1_000_000, 200_000, 200},
		// Rounds UP: a share that rounds down is a dataset quietly smaller than the
		// cap it was allowed.
		{3, 2, 667},
	} {
		if got := share(tc.rows, tc.budget); got != tc.want {
			t.Errorf("share(%d, %d) = %d, want %d", tc.rows, tc.budget, got, tc.want)
		}
	}
}

// TestTrimDropsTheWholeTrailingSubject. Half a subject on one side of a split is
// exactly the entity leak the grouping prevents, so a truncated read gives up the
// partial subject entirely.
func TestTrimDropsTheWholeTrailingSubject(t *testing.T) {
	in := facts([2]int{0, 1}, [2]int{0, 2}, [2]int{1, 3}, [2]int{1, 4}, [2]int{1, 5})
	got := trim(in, true)
	if len(got) != 2 {
		t.Fatalf("trim kept %d facts, want the 2 belonging to the complete subject", len(got))
	}
	for _, f := range got {
		if f.Subject != "a" {
			t.Fatalf("trim kept %q from the partial trailing subject", f.Subject)
		}
	}
	if len(trim(in, false)) != len(in) {
		t.Fatal("trim dropped facts from a read that did not hit its limit")
	}
	if len(trim(nil, true)) != 0 {
		t.Fatal("trim of nothing is not nothing")
	}
	// A read that returned exactly one subject and hit the limit has nothing
	// complete in it, and says so by returning nothing.
	if got := trim(facts([2]int{0, 1}, [2]int{0, 2}), true); len(got) != 0 {
		t.Fatalf("a single truncated subject was kept: %d facts", len(got))
	}
}

// TestCountReportsSubjectsAndAnHonestZeroForJudged. Every row this plane writes
// is unjudged, and reporting a judged count of zero rather than omitting it is
// what lets a model plane refuse to rank instead of naming a winner.
func TestCountReportsSubjectsAndAnHonestZeroForJudged(t *testing.T) {
	rows := cut(facts([2]int{0, 10}, [2]int{0, 20}, [2]int{1, 80}, [2]int{2, 90}), edges)
	c := count(rows)
	if c.Rows != 4 || c.Subjects != 3 {
		t.Fatalf("counts = %+v, want 4 rows over 3 subjects", c)
	}
	if c.Train+c.Val+c.Test != c.Rows {
		t.Fatalf("the splits do not add up: %+v", c)
	}
	if c.Judged != 0 || c.Productive != 0 || c.Unproductive != 0 {
		t.Fatalf("a dataset with no label plane reported judged rows: %+v", c)
	}
}
