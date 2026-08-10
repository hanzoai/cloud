package label

// resolve_test.go exercises the two properties the whole plane rests on, on the
// pure core, where they are decidable without a store, a clock or a wire.
//
// Every test here was written by reintroducing the defect and checking that THIS
// test — not some other one — goes red.

import (
	"strings"
	"testing"
	"time"
)

var epoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// fact builds a complete, admitted assertion, WRITTEN on the day it became
// knowable — a live pipeline, which is what this plane is for. Using admit()
// rather than a struct literal means every fixture below carries a real content
// digest, so the deterministic tie-break is exercised with the values production
// would compute.
//
// The write instant is a parameter of the fixture and not a convenience, because
// it is half the guard: Knowable is the later of the filer's `seen` and the
// server clock at the write, so a fixture that admitted everything at one late
// instant would be testing a plane that learned all of its history at once. That
// is the BACKFILL case, and it has its own test rather than being the accidental
// shape of every other one.
func fact(t *testing.T, src Source, d Disposition, atDays, seenDays int, conf float64) Fact {
	t.Helper()
	at := epoch.AddDate(0, 0, atDays)
	seen := epoch.AddDate(0, 0, seenDays)
	f, err := admit(Fact{
		Kind: KindTransaction, Subject: "tx-1", At: at,
		Seen: seen, Disposition: d, Source: src,
		Evidence: "ev-" + string(src), By: "svc", Confidence: conf,
	}, seen)
	if err != nil {
		t.Fatalf("admit: %v", err)
	}
	return f
}

// ── LATENCY: the label that did not exist yet ────────────────────────────────

// TestAsOfExcludesALabelThatDidNotExistYet is the no-leakage test.
//
// A transaction on day 0. An analyst reviews it on day 3 and calls it clean. The
// chargeback lands on day 200 — past a 120-day horizon. A materialisation for
// that transaction must see the day-3 review and MUST NOT see the day-200
// chargeback, because a model deciding on that transaction could not have.
//
// Joining on event time alone gives an offline AUC near 0.98 and an online model
// worth nothing. This is the test that says it cannot happen.
func TestAsOfExcludesALabelThatDidNotExistYet(t *testing.T) {
	review := fact(t, Review, Unproductive, 0, 3, 0.5)
	late := fact(t, Dispute, Productive, 0, 200, 1)
	facts := []Fact{review, late}

	w := Window{Now: epoch.AddDate(0, 0, 400), Horizon: 120 * 24 * time.Hour}
	got, ok := Resolve(facts, w.AsOf(epoch))
	if !ok {
		t.Fatal("nothing resolved at the horizon; the day-3 review was knowable")
	}
	if got.Winner.Source != Review {
		t.Fatalf("winner = %q, want the day-3 review: the day-200 dispute LEAKED into a 120-day horizon", got.Winner.Source)
	}
	if got.Disposition() != Unproductive {
		t.Fatalf("disposition = %q, want unproductive", got.Disposition())
	}
	// The leak must not reappear as a conflict either. Naming an assertion that
	// was not knowable yet leaks its existence into a past decision just as
	// surely as returning it as the winner.
	for _, c := range got.Conflicts {
		if c.Source == Dispute {
			t.Fatal("the unknowable dispute was named as a conflict, which leaks that it exists")
		}
	}
	if got.Contested {
		t.Fatal("contested is true, so the leaked assertion was counted after all")
	}

	// Past the dispute's own observation time it wins, which proves the exclusion
	// above was the HORIZON and not a bug that drops disputes.
	later, _ := Resolve(facts, epoch.AddDate(0, 0, 201))
	if later.Winner.Source != Dispute {
		t.Fatalf("at day 201 the winner = %q, want the dispute", later.Winner.Source)
	}
	if !later.Contested {
		t.Fatal("a productive dispute over an unproductive review is not reported as contested")
	}
}

// TestTheGuardDoesNotTakeTheFilersWordForIt is the regression for a leak the
// horizon could not see.
//
// `seen` is whatever the caller sent. A dispute filed TODAY with seen == at —
// the natural integration mistake, "the dispute is about this transaction" — used
// to be knowable a year before the row existed, so a backtest standing two days
// after the event resolved it as the winner and the whole horizon was decorative.
// Nothing tied the declared instant to any fact the server observed.
//
// Knowable is that tie: the later of the claim and the clock at the write. The
// same fact filed by a live pipeline the day it was adjudicated is unaffected —
// which is the second half of this test, and the reason the fix is a derivation
// and not a refusal.
func TestTheGuardDoesNotTakeTheFilersWordForIt(t *testing.T) {
	at := epoch
	// Filed on day 300, claiming to have been knowable on day 0.
	backdated, err := admit(Fact{
		Kind: KindTransaction, Subject: "tx-1", At: at, Seen: at,
		Disposition: Productive, Source: Dispute, Evidence: "dp-1", By: "svc", Confidence: 1,
	}, epoch.AddDate(0, 0, 300))
	if err != nil {
		t.Fatal(err)
	}
	if !backdated.Knowable.Equal(epoch.AddDate(0, 0, 300)) {
		t.Fatalf("knowable = %s, want the write instant: a claim older than the record cannot make the plane older", backdated.Knowable)
	}

	// A backtest standing on day 2 — 298 days before the row was written.
	if r, ok := Resolve([]Fact{backdated}, epoch.AddDate(0, 0, 2)); ok {
		t.Fatalf("a row written on day 300 resolved on day 2 as %q from %q: the guard is only as strong as the filer's honesty",
			r.Disposition(), r.Winner.Source)
	}
	// And on day 301 it is in force, so the exclusion above was the guard and not
	// a bug that drops backdated rows.
	if _, ok := Resolve([]Fact{backdated}, epoch.AddDate(0, 0, 301)); !ok {
		t.Fatal("the row is invisible even after it was written")
	}

	// THE LIVE PIPELINE IS UNTOUCHED. The same assertion, filed the day it was
	// adjudicated, is knowable on that day and resolves at the event's own
	// 120-day as-of exactly as before.
	live, err := admit(Fact{
		Kind: KindTransaction, Subject: "tx-1", At: at, Seen: epoch.AddDate(0, 0, 40),
		Disposition: Productive, Source: Dispute, Evidence: "dp-1", By: "svc", Confidence: 1,
	}, epoch.AddDate(0, 0, 40))
	if err != nil {
		t.Fatal(err)
	}
	if !live.Knowable.Equal(live.Seen) {
		t.Fatalf("knowable = %s, want the filer's own instant %s for a row written when it was adjudicated", live.Knowable, live.Seen)
	}
	if _, ok := Resolve([]Fact{live}, epoch.AddDate(0, 0, 120)); !ok {
		t.Fatal("a chargeback adjudicated on day 40 is not visible at the event's own 120-day as-of")
	}
}

// TestTheWinnerWithinARankIsDecidedByAServerObservedInstant. The second term of
// the total order was the caller's `seen` too, so two equally-ranked claims were
// ordered by a value either filer could set to anything. A correction wins from
// when it became knowable — and "knowable" has to mean the same thing there as it
// does in the visibility guard, or a caller could make a stale claim outrank the
// correction that replaced it.
func TestTheWinnerWithinARankIsDecidedByAServerObservedInstant(t *testing.T) {
	// Both from the same source, so rank cannot separate them. The correction is
	// written second and claims an EARLIER seen than the original.
	first, err := admit(Fact{Kind: KindTransaction, Subject: "tx-1", At: epoch,
		Seen: epoch.AddDate(0, 0, 40), Disposition: Productive, Source: Dispute,
		Evidence: "dp-1", By: "svc", Confidence: 1}, epoch.AddDate(0, 0, 40))
	if err != nil {
		t.Fatal(err)
	}
	correction, err := admit(Fact{Kind: KindTransaction, Subject: "tx-1", At: epoch,
		Seen: epoch.AddDate(0, 0, 1), Disposition: Unproductive, Source: Dispute,
		Evidence: "dp-1-reversed", By: "svc", Confidence: 1}, epoch.AddDate(0, 0, 90))
	if err != nil {
		t.Fatal(err)
	}
	got, ok := Resolve([]Fact{first, correction}, epoch.AddDate(0, 0, 120))
	if !ok {
		t.Fatal("nothing resolved")
	}
	if got.Winner.Evidence != "dp-1-reversed" {
		t.Fatalf("winner = %q; the claim the plane learned LAST is the one in force, whatever either filer declared",
			got.Winner.Evidence)
	}
}

// TestMaturityKeepsAnUnripeRowOutEntirely pins the other half of the leakage
// rule. A row whose horizon has not closed must not be admitted at all — not as
// a negative, which is what it would silently become if only the label join were
// filtered.
func TestMaturityKeepsAnUnripeRowOutEntirely(t *testing.T) {
	w := Window{Now: epoch.AddDate(0, 0, 30), Horizon: 120 * 24 * time.Hour}
	if w.Matured(epoch) {
		t.Fatal("a day-0 event is matured 30 days in under a 120-day horizon")
	}
	if got := Group([]Fact{fact(t, Review, Unproductive, 0, 1, 1)}, w); len(got) != 0 {
		t.Fatalf("Group admitted %d unmatured rows into the cohort", len(got))
	}
	// Exactly at the horizon it is admitted: the boundary is inclusive, so a
	// 120-day horizon means 120 days and not 121.
	at120 := Window{Now: epoch.AddDate(0, 0, 120), Horizon: 120 * 24 * time.Hour}
	if !at120.Matured(epoch) {
		t.Fatal("an event exactly at its horizon is not matured; the boundary is off by one")
	}
}

// TestEachRowObservesItsOwnHorizon pins the per-row as-of. One as-of over a batch
// would hand a January row the same knowledge as a June row, and the January row
// would be trained on five extra months of hindsight.
func TestEachRowObservesItsOwnHorizon(t *testing.T) {
	january, err := admit(Fact{
		Kind: KindTransaction, Subject: "tx-jan", At: epoch,
		Seen: epoch.AddDate(0, 0, 150), Disposition: Productive, Source: Dispute,
		Evidence: "d1", By: "svc", Confidence: 1,
	}, epoch.AddDate(0, 0, 150))
	if err != nil {
		t.Fatal(err)
	}
	june, err := admit(Fact{
		Kind: KindTransaction, Subject: "tx-jun", At: epoch.AddDate(0, 0, 150),
		Seen: epoch.AddDate(0, 0, 160), Disposition: Productive, Source: Dispute,
		Evidence: "d2", By: "svc", Confidence: 1,
	}, epoch.AddDate(0, 0, 160))
	if err != nil {
		t.Fatal(err)
	}

	// A 120-day horizon: the January dispute was knowable on day 150, which is
	// AFTER January's own as-of (day 120), so tx-jan stays unlabelled. The June
	// dispute was knowable 10 days after its event, well inside its own as-of.
	//
	// BOTH events are matured at day 400, so both are in the cohort — the one
	// that resolves and the one that does not. A grouping that returned only the
	// resolvable events would make `matured` count what was LABELLED.
	w := Window{Now: epoch.AddDate(0, 0, 400), Horizon: 120 * 24 * time.Hour}
	got := Group([]Fact{january, june}, w)
	if len(got) != 2 {
		t.Fatalf("the matured cohort holds %d events, want both", len(got))
	}
	labelled := map[string]bool{}
	for _, c := range got {
		labelled[c.Subject] = c.Labelled
	}
	if !labelled["tx-jun"] {
		t.Fatal("tx-jun's dispute was knowable 10 days after the event and inside its own as-of, yet it did not resolve")
	}
	if labelled["tx-jan"] {
		t.Fatal("tx-jan resolved; its dispute arrived after tx-jan's own horizon closed, so a shared as-of leaked")
	}
}

// ── CONFLICT: two sources disagree ───────────────────────────────────────────

// TestPrecedenceResolvesAConflictAndKeepsTheLoser is the conflicting-label test.
//
// An analyst reviewed a transaction and called it clean. Weeks later the card
// network charged it back. Both assertions are true statements about who said
// what; the plane must pick the adjudicated one, keep the analyst's, and say the
// event is contested.
func TestPrecedenceResolvesAConflictAndKeepsTheLoser(t *testing.T) {
	review := fact(t, Review, Unproductive, 0, 3, 1)  // high confidence, weak source
	dispute := fact(t, Dispute, Productive, 0, 40, 0) // no confidence, strong source

	got, ok := Resolve([]Fact{review, dispute}, epoch.AddDate(0, 0, 120))
	if !ok {
		t.Fatal("nothing resolved")
	}
	if got.Winner.Source != Dispute {
		t.Fatalf("winner = %q, want dispute: an analyst's confident hunch outranked a card network", got.Winner.Source)
	}
	if !got.Contested {
		t.Fatal("two assertions with different dispositions are not reported as contested")
	}
	if len(got.Conflicts) != 1 || got.Conflicts[0].Source != Review {
		t.Fatalf("the losing assertion was dropped instead of kept: %+v", got.Conflicts)
	}
	// The loser keeps its whole provenance, which is what an adverse action has
	// to be able to show.
	if got.Conflicts[0].Evidence == "" || got.Conflicts[0].By == "" {
		t.Fatal("the losing assertion lost its provenance, so nobody can say what it was")
	}
}

// TestAgreementIsNotConflict pins the other direction. Two sources concluding the
// same thing is corroboration; reporting it as contested would make the number
// useless for spotting a genuinely disputed label.
func TestAgreementIsNotConflict(t *testing.T) {
	got, _ := Resolve([]Fact{
		fact(t, Review, Productive, 0, 3, 1),
		fact(t, Dispute, Productive, 0, 40, 1),
	}, epoch.AddDate(0, 0, 120))
	if got.Contested {
		t.Fatal("two sources that AGREE were reported as contested")
	}
	if len(got.Conflicts) != 1 {
		t.Fatalf("the corroborating assertion was dropped: %+v", got.Conflicts)
	}
}

// TestPrecedenceIsATotalOrderAcrossEveryPair walks every ordered pair of sources
// and asserts the stronger one wins regardless of the order they arrive in.
// Resolve sorts, so a comparator that was not a strict weak ordering would give
// an answer that depends on input order — and a materialisation that depends on
// input order is not reproducible.
func TestPrecedenceIsATotalOrderAcrossEveryPair(t *testing.T) {
	all := sources()
	for i, strong := range all {
		for _, weak := range all[i+1:] {
			a := fact(t, strong, Productive, 0, 10, 0)
			b := fact(t, weak, Unproductive, 0, 10, 1)
			forward, _ := Resolve([]Fact{a, b}, epoch.AddDate(0, 0, 120))
			backward, _ := Resolve([]Fact{b, a}, epoch.AddDate(0, 0, 120))
			if forward.Winner.Source != strong {
				t.Errorf("%q lost to %q", strong, weak)
			}
			if forward.Winner.ID != backward.Winner.ID {
				t.Errorf("%q vs %q: the winner depends on arrival order", strong, weak)
			}
		}
	}
}

// TestACorrectionWinsOnlyFromWhenItWasKnowable is the reason a correction is a
// new assertion rather than an update.
//
// Commerce files a dispute on day 40, then reverses it on day 90 (the merchant
// won the representment). A model trained on day 60 must still see the day-40
// claim; a model trained on day 120 must see the reversal. An UPDATE would
// destroy the first answer and make the day-60 training set unreproducible.
func TestACorrectionWinsOnlyFromWhenItWasKnowable(t *testing.T) {
	first := fact(t, Dispute, Productive, 0, 40, 1)
	fixed := fact(t, Dispute, Unproductive, 0, 90, 1)
	facts := []Fact{first, fixed}

	early, _ := Resolve(facts, epoch.AddDate(0, 0, 60))
	if early.Disposition() != Productive {
		t.Fatalf("as of day 60 the disposition = %q; the day-90 correction leaked backwards", early.Disposition())
	}
	late, _ := Resolve(facts, epoch.AddDate(0, 0, 120))
	if late.Disposition() != Unproductive {
		t.Fatalf("as of day 120 the disposition = %q; the correction never took effect", late.Disposition())
	}
	if !late.Contested {
		t.Fatal("a source that reversed itself is not reported as contested")
	}
}

// TestTiesBreakDeterministically pins the last two terms. Two assertions from one
// source at one instant with one confidence must still resolve to the same winner
// on every run, or two materialisations of one spec disagree.
func TestTiesBreakDeterministically(t *testing.T) {
	a, err := admit(Fact{Kind: KindAccount, Subject: "a1", At: epoch, Seen: epoch,
		Disposition: Productive, Source: Review, Evidence: "aaa", By: "x", Confidence: 0.5}, epoch)
	if err != nil {
		t.Fatal(err)
	}
	b, err := admit(Fact{Kind: KindAccount, Subject: "a1", At: epoch, Seen: epoch,
		Disposition: Unproductive, Source: Review, Evidence: "bbb", By: "x", Confidence: 0.5}, epoch)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := Resolve([]Fact{a, b}, epoch)
	for i := range 50 {
		got, _ := Resolve([]Fact{b, a}, epoch)
		if got.Winner.ID != want.Winner.ID {
			t.Fatalf("run %d picked %q, first run picked %q", i, got.Winner.ID, want.Winner.ID)
		}
	}
	lo, hi := a.ID, b.ID
	if lo > hi {
		lo, hi = hi, lo
	}
	if want.Winner.ID != lo {
		t.Fatalf("the tie broke on %q, want the lower digest %q", want.Winner.ID, lo)
	}
}

// TestNothingKnowableIsNotAnAnswer pins the honest gap. An event nobody has
// judged yet must resolve to NOTHING, never to unproductive — manufacturing
// negatives is how a fraud model comes to describe its own block list.
func TestNothingKnowableIsNotAnAnswer(t *testing.T) {
	if _, ok := Resolve(nil, epoch); ok {
		t.Fatal("an empty assertion set produced an answer")
	}
	future := fact(t, Dispute, Productive, 0, 200, 1)
	if _, ok := Resolve([]Fact{future}, epoch.AddDate(0, 0, 10)); ok {
		t.Fatal("an assertion nobody could see yet produced an answer")
	}
}

// TestAnUnknownSourceSortsLastRatherThanFirst pins the safe direction of rank()'s
// fallback. A row written under a source since retired must not outrank a
// recognised one.
func TestAnUnknownSourceSortsLastRatherThanFirst(t *testing.T) {
	known := fact(t, Sample, Unproductive, 0, 1, 0)
	stray := known
	stray.Source = "an-old-source"
	stray.Disposition = Productive
	stray.ID = digest(stray)
	got, _ := Resolve([]Fact{stray, known}, epoch.AddDate(0, 0, 10))
	if got.Winner.Source != Sample {
		t.Fatalf("an unrecognised source won with %q", got.Winner.Source)
	}
}

// ── the vocabulary ───────────────────────────────────────────────────────────

// TestDispositionsAreTheEnginesOwnSpelling pins the three literals against
// luxfi/aml pkg/replay. A drift here silently halves a training set there, and
// leaves topology.Search unable to name a winner — the one thing it refuses to
// fake.
func TestDispositionsAreTheEnginesOwnSpelling(t *testing.T) {
	if string(Productive) != "productive" || string(Unproductive) != "unproductive" || string(Unjudged) != "" {
		t.Fatalf("the vocabulary drifted from the engine's: %q %q %q", Productive, Unproductive, Unjudged)
	}
	if Productive.code() != 1 || Unproductive.code() != 0 || Unjudged.code() != -1 {
		t.Fatal("the columnar encoding drifted from the documented 1/0/-1")
	}
}

// TestPublishedPrecedenceIsTheEnforcedPrecedence pins sources() against rank().
// A published order that merely DESCRIBES the rule can drift from it; this makes
// them one value.
func TestPublishedPrecedenceIsTheEnforcedPrecedence(t *testing.T) {
	got := sources()
	if len(got) != len(precedence) {
		t.Fatalf("sources() publishes %d of %d", len(got), len(precedence))
	}
	for i := 1; i < len(got); i++ {
		a, _ := rank(got[i-1])
		b, _ := rank(got[i])
		if a >= b {
			t.Fatalf("published order is not the enforced order at %d: %q(%d) before %q(%d)", i, got[i-1], a, got[i], b)
		}
	}
}

// TestTheColumnarOrderingNamesEverySource is the only defence available against
// a rule with two homes. The Go comparator and the ClickHouse ordering tuple are
// both rendered from `precedence`; if a source is added and the SQL rendering
// forgets it, that source silently sorts last in the warehouse and first-class in
// the app — two different training sets from one rule.
func TestTheColumnarOrderingNamesEverySource(t *testing.T) {
	sql := Order()
	for s, r := range precedence {
		if !strings.Contains(sql, "'"+string(s)+"'") {
			t.Errorf("the columnar ordering does not name source %q", s)
		}
		_ = r
	}
	// The tie-breaks must match stronger() term for term, in order — including
	// the DERIVED instant. A warehouse ordering on `seen` while the Go comparator
	// orders on `knowable` is one rule with two answers.
	for _, want := range []string{"-toInt64(toUnixTimestamp(knowable))", "-confidence", "id"} {
		if !strings.Contains(sql, want) {
			t.Errorf("the columnar ordering is missing the %q term that stronger() applies", want)
		}
	}
	if !strings.Contains(ResolvedSQL(), "WHERE org = ?") {
		t.Error("the warehouse read does not open with a bound org predicate")
	}
	if !strings.Contains(ResolvedSQL(), "knowable <= at + ?") {
		t.Error("the warehouse read does not apply the as-of horizon to the SERVER-DERIVED instant, so a training join there would resolve under a weaker rule than the record plane")
	}
	// And it must not still be reading the declared instant, which is the value
	// the record plane stopped trusting.
	if strings.Contains(ResolvedSQL(), "seen <= at + ?") {
		t.Error("the warehouse read still applies the horizon to the filer's declared `seen`")
	}
}

// TestTheDerivedInstantReachesTheWarehouse. The record plane derives Knowable and
// the guard reads it; if the column is not in the columnar copy, the warehouse
// cannot apply the same rule at all and a materialiser joining there trains on
// rows the resolve op refuses to return.
func TestTheDerivedInstantReachesTheWarehouse(t *testing.T) {
	if !strings.Contains(labelDDL, "knowable    DateTime") {
		t.Fatal("the derived copy has no `knowable` column, so the warehouse cannot check the leakage rule at all")
	}
	f := fact(t, Dispute, Productive, 0, 40, 1)
	stmt, args := insert(tenantFor(t), []Fact{f})
	if !strings.Contains(stmt, "knowable") {
		t.Fatalf("the columnar write does not carry the derived instant: %s", stmt)
	}
	found := false
	for _, a := range args {
		if ts, ok := a.(time.Time); ok && ts.Equal(f.Knowable) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the derived instant %s is not among the bound values", f.Knowable)
	}
}

// ── admission ────────────────────────────────────────────────────────────────

// TestAdmitRefusesEveryUndefendableAssertion walks the gate. Each refusal is a
// label somebody would otherwise have had to defend in front of a regulator as
// though a human meant it.
func TestAdmitRefusesEveryUndefendableAssertion(t *testing.T) {
	now := epoch.AddDate(0, 0, 10)
	ok := Fact{Kind: KindTransaction, Subject: "tx", At: epoch, Seen: epoch,
		Disposition: Productive, Source: Dispute, Evidence: "d-1", By: "svc", Confidence: 1}
	if _, err := admit(ok, now); err != nil {
		t.Fatalf("a complete assertion was refused: %v", err)
	}
	for _, tc := range []struct {
		name string
		mut  func(*Fact)
	}{
		{"unknown kind", func(f *Fact) { f.Kind = "wallet" }},
		{"no subject", func(f *Fact) { f.Subject = "  " }},
		{"unknown disposition", func(f *Fact) { f.Disposition = "fraud" }},
		{"no source", func(f *Fact) { f.Source = "" }},
		{"unranked source", func(f *Fact) { f.Source = "vendor" }},
		{"no evidence", func(f *Fact) { f.Evidence = "" }},
		{"no event time", func(f *Fact) { f.At = time.Time{} }},
		{"no observation time", func(f *Fact) { f.Seen = time.Time{} }},
		{"seen before the event it judges", func(f *Fact) { f.Seen = epoch.AddDate(0, 0, -1) }},
		{"event in the future", func(f *Fact) { f.At = now.AddDate(0, 0, 1); f.Seen = now.AddDate(0, 0, 1) }},
		{"label not knowable yet", func(f *Fact) { f.Seen = now.AddDate(0, 0, 1) }},
		{"confidence above one", func(f *Fact) { f.Confidence = 1.5 }},
		{"confidence below zero", func(f *Fact) { f.Confidence = -0.1 }},
		{"subject longer than the bound", func(f *Fact) { f.Subject = strings.Repeat("s", subjectMax+1) }},
		{"evidence longer than the bound", func(f *Fact) { f.Evidence = strings.Repeat("e", evidenceMax+1) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := ok
			tc.mut(&bad)
			if _, err := admit(bad, now); err == nil {
				t.Fatalf("admitted an assertion with %s", tc.name)
			}
		})
	}
}

// TestTheDigestIsTheIdempotenceKey pins what "the same fact" means. The server
// clock must not be in it, or every webhook redelivery would be a new row and
// the plane would grow one record per retry.
func TestTheDigestIsTheIdempotenceKey(t *testing.T) {
	// Seen sits two days after At so that a one-day mutation of either stays a
	// legal assertion; the point of this test is the digest, not the gate.
	base := Fact{Kind: KindTransaction, Subject: "tx", At: epoch, Seen: epoch.AddDate(0, 0, 2),
		Disposition: Productive, Source: Dispute, Evidence: "d-1", By: "svc", Confidence: 1}
	now := epoch.AddDate(0, 0, 10)
	a, err := admit(base, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := admit(base, now.AddDate(0, 0, 9)) // a later redelivery
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID {
		t.Fatal("a redelivery of the same fact produced a different id, so retries would multiply rows")
	}
	if a.Wrote.Equal(b.Wrote) {
		t.Fatal("Wrote did not move, so the retention clock is not the server's")
	}
	// Every semantic field must move the digest, or two different assertions
	// would silently collapse into one.
	for _, mut := range []func(*Fact){
		func(f *Fact) { f.Kind = KindAccount },
		func(f *Fact) { f.Subject = "tx-2" },
		func(f *Fact) { f.At = epoch.AddDate(0, 0, 1) },
		func(f *Fact) { f.Seen = epoch.AddDate(0, 0, 3) },
		func(f *Fact) { f.Disposition = Unproductive },
		func(f *Fact) { f.Source = Review },
		func(f *Fact) { f.Evidence = "d-2" },
		func(f *Fact) { f.By = "someone-else" },
		func(f *Fact) { f.Confidence = 0.5 },
	} {
		other := base
		mut(&other)
		got, err := admit(other, now)
		if err != nil {
			t.Fatalf("%+v: %v", other, err)
		}
		if got.ID == a.ID {
			t.Fatalf("a changed field did not move the digest: %+v", other)
		}
	}
	// And the fold must be injective across a boundary: shifting a character
	// from one field to the next must not produce the same digest.
	x := base
	x.Subject, x.Evidence = "tx", "d-1"
	y := base
	y.Subject, y.Evidence = "txd", "-1"
	fx, _ := admit(x, now)
	fy, _ := admit(y, now)
	if fx.ID == fy.ID {
		t.Fatal("the digest fold is not injective across a field boundary")
	}
}
