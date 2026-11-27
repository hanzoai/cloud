package graph

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/cloud/claim"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// year is the middle of a year, the instant a question about that year asks at.
func year(y int) time.Time { return time.Date(y, 7, 1, 0, 0, 0, 0, time.UTC) }

// term is one statement with its own lifetime, filed at wrote: [from, to) of
// valid time, heard at wrote. from and to are whole years, the way a knowledge
// base states a term of office.
func term(t testing.TB, entity, relation, value string, from, to int, wrote time.Time) Fact {
	t.Helper()
	f := Fact{
		Entity: entity, Relation: relation, Value: value, Names: true,
		At: time.Date(from, 1, 1, 0, 0, 0, 0, time.UTC), Source: "kb", Evidence: "kb:1", By: "tester",
	}
	if to != 0 {
		f.Until = time.Date(to+1, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	f, err := admit(f, wrote)
	if err != nil {
		t.Fatalf("admit %s %s %s: %v", entity, relation, value, err)
	}
	f.Wrote = wrote
	f.Knowable = claim.Knowable(f.Seen, f.Wrote)
	f.ID = digest(f)
	return f
}

func holders(held []Fact) []string {
	out := make([]string, 0, len(held))
	for _, f := range held {
		out = append(out, f.Value)
	}
	return out
}

// TestHistoryFiledTodayAnswersAboutItsOwnTime is the defect CronQuestions found:
// a fact about 1995 filed today is knowable today, and a read that bounded the
// knowable instant by the year asked about found nothing. The year asked about
// is valid time; what was heard is the other clock.
func TestHistoryFiledTodayAnswersAboutItsOwnTime(t *testing.T) {
	now := time.Now().UTC()
	facts := []Fact{term(t, "leeds", "coach", "howard", 1988, 1996, now)}
	held, _ := Resolve(facts, when{valid: year(1995), known: now}, false)
	if len(held) != 1 || held[0].Value != "howard" {
		t.Fatalf("1995 as known now = %v, want howard", holders(held))
	}
	if held, _ := Resolve(facts, when{valid: year(1997), known: now}, false); len(held) != 0 {
		t.Errorf("1997 is past the term's until, yet %v holds", holders(held))
	}
	if held, _ := Resolve(facts, when{valid: year(1995), known: now.Add(-time.Hour)}, false); len(held) != 0 {
		t.Errorf("an hour before it was filed nothing was known, yet %v holds", holders(held))
	}
}

// TestOneTermEndingKeepsTheOtherHolders: a position read from its own side holds
// every holder whose term covers the year. One term ending — Leeds 1993–97 —
// must not end another that began that year, which is what a retraction of the
// whole pair did.
func TestOneTermEndingKeepsTheOtherHolders(t *testing.T) {
	now := time.Now().UTC()
	facts := []Fact{
		term(t, "mark", "plays_for", "leeds", 1993, 1997, now),
		term(t, "mark", "plays_for", "burnley", 1997, 2000, now),
		term(t, "mark", "plays_for", "derby", 1990, 1992, now),
	}
	for _, c := range []struct {
		y    int
		want []string
	}{
		{1991, []string{"derby"}},
		{1995, []string{"leeds"}},
		{1997, []string{"burnley", "leeds"}},
		{1998, []string{"burnley"}},
		{2005, nil},
	} {
		held, conflicts := Resolve(facts, when{valid: year(c.y), known: now}, false)
		if got := holders(held); fmt.Sprint(got) != fmt.Sprint(c.want) {
			t.Errorf("%d: %v, want %v", c.y, got, c.want)
		}
		if len(conflicts) != 0 {
			t.Errorf("%d: an edge pair holding several is not contested: %v", c.y, holders(conflicts))
		}
	}
}

// TestKnowledgeAndTheWorldMoveApart: a correction of when a term ended is filed
// later. As known before it, the old account holds; as known after, the new one
// — and asking about another year moves neither.
func TestKnowledgeAndTheWorldMoveApart(t *testing.T) {
	filed := time.Now().UTC().Add(-2 * time.Hour)
	corrected := filed.Add(time.Hour)
	facts := []Fact{
		term(t, "ann", "chairs", "board", 2010, 0, filed),
		term(t, "ann", "chairs", "board", 2010, 2014, corrected),
	}
	before := when{valid: year(2016), known: filed.Add(time.Minute)}
	after := when{valid: year(2016), known: corrected.Add(time.Minute)}
	if held, _ := Resolve(facts, before, false); len(held) != 1 {
		t.Errorf("as known before the correction the open term holds in 2016: %v", holders(held))
	}
	if held, _ := Resolve(facts, after, false); len(held) != 0 {
		t.Errorf("as known after it, the term ended in 2014: %v", holders(held))
	}
	if held, _ := Resolve(facts, when{valid: year(2012), known: after.known}, false); len(held) != 1 {
		t.Errorf("2012 is inside the corrected term: %v", holders(held))
	}
}

// TestAOneValuedPairTakesTheLatestStatement: a property holds one value at a
// time, so a later statement ends an earlier one, and a statement past its own
// until leaves the pair holding nothing rather than falling back.
func TestAOneValuedPairTakesTheLatestStatement(t *testing.T) {
	now := time.Now().UTC()
	status := func(v string, from, to int) Fact {
		f := term(t, "svc", "status", v, from, to, now)
		f.Names = false
		f.ID = digest(f)
		return f
	}
	facts := []Fact{status("alpha", 2019, 0), status("beta", 2021, 0), status("sunset", 2023, 2024)}
	for y, want := range map[int]string{2018: "", 2020: "alpha", 2022: "beta", 2023: "sunset", 2026: ""} {
		held, _ := Resolve(facts, when{valid: year(y), known: now}, true)
		got := ""
		if len(held) == 1 {
			got = held[0].Value
		}
		if got != want {
			t.Errorf("%d: %q, want %q", y, got, want)
		}
	}
}

// TestADiffOfTheWorld: holding the knowledge instant still, a diff between two
// years is what began and ended between them.
func TestADiffOfTheWorld(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		now := time.Now().UTC()
		leeds := term(t, "mark", "plays_for", "leeds", 1993, 1997, now)
		burnley := term(t, "mark", "plays_for", "burnley", 1997, 2000, now)
		record(t, s, leeds, burnley)
		out, cut, err := s.diff(context.Background(), "", when{valid: year(1995), known: now}, when{valid: year(1999), known: now}, walkBound)
		if err != nil || cut {
			t.Fatalf("diff: %v cut=%v", err, cut)
		}
		kinds := map[string]string{}
		for _, d := range out {
			kinds[d.kind] = d.now.Value
		}
		if kinds["asserted"] != "burnley" || kinds["retracted"] != "leeds" || len(out) != 2 {
			t.Errorf("1995→1999 = %+v, want burnley asserted and leeds ended", out)
		}
	})
}

// TestTheFirstInstantIsAnInstant: 0001-01-01T00:00:00Z is Go's zero time, and
// asking IsZero of it refused a real date as though none had been sent. Absent
// is refused where it can be told apart — at the wire.
func TestTheFirstInstantIsAnInstant(t *testing.T) {
	f, err := fromWire(graphFact{Entity: "e", Relation: "r", Value: "v", At: "0001-01-01T00:00:00Z", Source: "s"}, "by", time.Now().UTC())
	if err != nil || !f.At.Equal(time.Date(1, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("the first instant was refused: %v", err)
	}
	if _, err := fromWire(graphFact{Entity: "e", Relation: "r", Value: "v", Source: "s"}, "by", time.Now().UTC()); err == nil {
		t.Error("an assertion with no at was admitted")
	}
}

// TestAReadSaysWhenItStopped: a read that hits its limit says so, as resolve
// and neighbors always have.
func TestAReadSaysWhenItStopped(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountGraph(t)
		for i := range 3 {
			assertFact(t, app, "", "acme/svc/api", fmt.Sprintf("r%d", i), "x", false)
		}
		for limit, want := range map[int]bool{2: true, 5: false} {
			code, b := call(t, app, http.MethodGet, fmt.Sprintf("/v1/graph?entity=acme/svc/api&limit=%d", limit), "", nil)
			var out graphReadOut
			if err := json.Unmarshal(b, &out); err != nil || code != http.StatusOK {
				t.Fatalf("read: %d %s", code, b)
			}
			if out.Truncated != want {
				t.Errorf("limit %d over 3 rows: truncated=%v, want %v", limit, out.Truncated, want)
			}
		}
	})
}

// TestATermEndingIn1969Ends: a term through 1969 ends at 1970-01-01T00:00:00Z,
// which is Unix zero. Stored as zero and read back, a sentinel that meant "open"
// made every such term hold for ever.
func TestATermEndingIn1969Ends(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s := newStore(t)
		now := time.Now().UTC()
		record(t, s, term(t, "ann", "chairs", "board", 1963, 1969, now))
		facts, err := s.read(context.Background(), filter{Entity: "ann", Relation: "chairs"})
		if err != nil || len(facts) != 1 {
			t.Fatalf("read: %v %d", err, len(facts))
		}
		if !facts[0].Until.Equal(time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("until = %v, want 1970-01-01", facts[0].Until)
		}
		if held, _ := Resolve(facts, when{valid: year(2014), known: now}, false); len(held) != 0 {
			t.Errorf("a term through 1969 still held in 2014: %v", holders(held))
		}
	})
}

// TestTwoTermsOfOneHolderBothHold: a holder with two overlapping terms is two
// statements, each with its own evidence. Keeping one — the oldest — made the
// most recently begun holder of a year the wrong one.
func TestTwoTermsOfOneHolderBothHold(t *testing.T) {
	now := time.Now().UTC()
	facts := []Fact{
		term(t, "post", "held_by", "ann", 1979, 1989, now),
		term(t, "post", "held_by", "ann", 1982, 1986, now),
		term(t, "post", "held_by", "bob", 1980, 1990, now),
	}
	held, _ := Resolve(facts, when{valid: year(1983), known: now}, false)
	if len(held) != 3 || held[0].Value != "ann" || held[0].At.Year() != 1982 {
		t.Errorf("1983 = %v, want both of ann's terms and bob's, ann's 1982 term first", holders(held))
	}
}

// TestTwoAccountsLearnedTogetherBothStand: the same holder from the same year
// twice, with different ends, filed together — two stints, not a correction.
// Picking one by digest dropped the longer one at random.
func TestTwoAccountsLearnedTogetherBothStand(t *testing.T) {
	now := time.Now().UTC()
	facts := []Fact{
		term(t, "rob", "plays_for", "leeds", 2001, 2004, now),
		term(t, "rob", "plays_for", "leeds", 2001, 2001, now),
	}
	if held, _ := Resolve(facts, when{valid: year(2003), known: now}, false); len(held) != 1 || held[0].Until.Year() != 2005 {
		t.Errorf("2003 = %v, want the 2001–2004 stint", holders(held))
	}
	if held, _ := Resolve(facts, when{valid: year(2001), known: now}, false); len(held) != 2 {
		t.Errorf("2001 = %v, want both stints", holders(held))
	}
}
