package label

// resolve.go is the RULE: which assertion is in force, as of when, and what
// disagreed with it. It is pure — facts in, an answer out, no store and no clock
// of its own — so the two properties the whole plane rests on are decidable by
// reading one file.
//
// THE TWO PROPERTIES.
//
//  1. NO LEAKAGE. An assertion is visible at an observation instant only if it
//     was knowable then, and "knowable" is Fact.Knowable — the later of what the
//     filer claimed and when this plane wrote the row, derived server-side. It is
//     deliberately NOT the caller's `seen`: a guard whose only input is a value
//     the caller chose is exactly as strong as the caller's honesty. Window states
//     the instant; visible() enforces it; every read path derives its instant from
//     Window and never from the wall clock. There is no second place to get this
//     wrong.
//
//  2. NO SILENT OVERWRITE. Two sources that disagree both stay. The winner is
//     chosen by an explicit total order and the losers are RETURNED — an adverse
//     action can therefore show that the plane knew of a contrary claim and say
//     why it lost, which is the difference between a defensible decision and an
//     assertion of one.

import (
	"sort"
	"time"
)

// Window is the observation a caller is resolving under, and it is the whole of
// the leakage guard.
//
// Now is the instant the caller is standing at — the materialisation instant for
// training, or a past instant for a backtest. Horizon is how long an event must
// age before it may be admitted at all.
//
// Both are values on the request, not globals, because a backtest that could not
// move Now would be a backtest that resolves labels with today's knowledge and
// reports a score no live model could ever have earned.
type Window struct {
	Now     time.Time
	Horizon time.Duration
}

// Matured reports whether an event at `at` has aged past the horizon. A row that
// has not may still be scored — the density model does not need a label — but it
// must not be admitted to a supervised training set as a negative, because the
// chargeback that would make it a positive has not had time to arrive.
func (w Window) Matured(at time.Time) bool { return !at.Add(w.Horizon).After(w.Now) }

// AsOf is the instant an event at `at` observes its labels at: the moment its
// horizon closes. A label seen after it is a label that did not exist when the
// model would have had to act, and it is invisible here.
func (w Window) AsOf(at time.Time) time.Time { return at.Add(w.Horizon) }

// visible reports whether an assertion was knowable to THIS PLANE at an instant.
// It reads the derived Knowable and never the declared Seen — see Fact.Knowable.
func visible(f Fact, asOf time.Time) bool { return !f.Knowable.After(asOf) }

// Resolved is the answer: what is in force, and what disagreed.
type Resolved struct {
	Kind    Kind
	Subject string
	At      time.Time
	// AsOf is the instant this answer was computed at. It is on the value rather
	// than only on the request, because a resolved label handed to a training set
	// or an auditor without the instant it was true at is a claim nobody can check.
	AsOf time.Time
	// Winner is the assertion in force. Its whole provenance travels with it —
	// source, evidence, asserter — because that is what an adverse action needs.
	Winner Fact
	// Conflicts is every OTHER visible assertion, in the same precedence order,
	// strongest first. It is horizon-filtered exactly like the winner: an
	// assertion that was not knowable yet cannot even be NAMED as a conflict,
	// because naming it would leak its existence into a past decision.
	Conflicts []Fact
	// Contested is true when at least one visible assertion claims a DIFFERENT
	// disposition from the winner. Two sources that agree are corroboration, not
	// conflict, and reporting them as conflict would make the number useless.
	Contested bool
}

// Disposition is the claim in force — the one question every consumer of a
// Resolved asks first. It is the winner's, never a vote or an average: an
// average of two adjudications is a third claim nobody made.
func (r Resolved) Disposition() Disposition { return r.Winner.Disposition }

// Resolve picks the assertion in force at asOf and reports what it beat.
//
// It reports false when nothing was knowable then — which is a real answer and
// not an error. "No label yet" is the ordinary state of a fresh transaction, and
// a plane that answered Unproductive there would be manufacturing negatives.
func Resolve(facts []Fact, asOf time.Time) (Resolved, bool) {
	seen := make([]Fact, 0, len(facts))
	for _, f := range facts {
		if visible(f, asOf) {
			seen = append(seen, f)
		}
	}
	if len(seen) == 0 {
		return Resolved{}, false
	}
	sort.SliceStable(seen, func(i, j int) bool { return stronger(seen[i], seen[j]) })

	out := Resolved{
		Kind:    seen[0].Kind,
		Subject: seen[0].Subject,
		At:      seen[0].At,
		AsOf:    asOf,
		Winner:  seen[0],
	}
	if len(seen) > 1 {
		out.Conflicts = seen[1:]
	}
	for _, f := range out.Conflicts {
		if f.Disposition != out.Winner.Disposition {
			out.Contested = true
			break
		}
	}
	return out, true
}

// stronger is the TOTAL ORDER, and every term of it is deliberate:
//
//  1. RANK. Adjudication weight, declared once in fact.go's precedence map. A
//     card network's chargeback outranks an analyst's hunch because a different
//     amount of process stands behind it.
//  2. LATER Knowable. Within one rank, the most recently knowable claim wins —
//     which is exactly how a SOURCE CORRECTING ITSELF works: the correction is a
//     new assertion, it wins from the moment it became knowable, and the original
//     still wins for every observation instant BEFORE that. A correction
//     therefore cannot retroactively change what a past decision knew. It is the
//     DERIVED instant here for the same reason it is in visible(): the term that
//     decides which of two equally-ranked claims is in force must not be one a
//     caller can set to any value it likes.
//  3. HIGHER Confidence. A stated confidence is only ever a tie-breaker; it
//     cannot lift a weak source above a strong one, or every caller would send 1.
//  4. LOWER ID. The content digest, so a tie is broken by a value both sides
//     compute identically rather than by whichever row the storage engine
//     happened to return. Determinism here is what makes a materialisation
//     reproducible.
//
// A total order matters more than any single term of it: two runs over one set of
// assertions must produce one training set, or nothing downstream is comparable.
func stronger(a, b Fact) bool {
	ra, _ := rank(a.Source)
	rb, _ := rank(b.Source)
	if ra != rb {
		return ra < rb
	}
	if !a.Knowable.Equal(b.Knowable) {
		return a.Knowable.After(b.Knowable)
	}
	if a.Confidence != b.Confidence {
		return a.Confidence > b.Confidence
	}
	return a.ID < b.ID
}

// Cohort is one MATURED event and what was knowable about it by its own as-of.
//
// Labelled is a field and not the absence of an entry, because "matured" and
// "judged" are two different counts and an operator divides one by the other. A
// grouping that returned only the resolvable events would make the denominator
// exclude exactly the numerator's complement — matured would count what was
// LABELLED, the ratio would read 1.0 on a plane with one label in it, and the
// gate on training would say go.
type Cohort struct {
	Kind    Kind
	Subject string
	At      time.Time
	// AsOf is this event's own observation instant: At plus the horizon.
	AsOf time.Time
	// Label is the assertion in force at AsOf, valid only when Labelled. An
	// unlabelled matured event is the ordinary state of most traffic and it is
	// never a negative: manufacturing one is how a fraud model comes to describe
	// the incumbent block list.
	Label    Resolved
	Labelled bool
}

// Group folds a flat set of assertions into one Cohort per MATURED event, each
// resolved at its OWN as-of instant.
//
// The per-event instant is the point. A single as-of over a batch would give a
// row from January and a row from June the same knowledge, and the January row
// would be trained on six extra months of hindsight. Every row observes exactly
// its own horizon.
func Group(facts []Fact, w Window) []Cohort {
	type key struct {
		kind    Kind
		subject string
		at      int64
	}
	by := map[key][]Fact{}
	order := []key{}
	for _, f := range facts {
		k := key{f.Kind, f.Subject, f.At.UTC().Unix()}
		if _, ok := by[k]; !ok {
			order = append(order, k)
		}
		by[k] = append(by[k], f)
	}
	out := make([]Cohort, 0, len(order))
	for _, k := range order {
		at := time.Unix(k.at, 0).UTC()
		if !w.Matured(at) {
			continue
		}
		c := Cohort{Kind: k.kind, Subject: k.subject, At: at, AsOf: w.AsOf(at)}
		c.Label, c.Labelled = Resolve(by[k], c.AsOf)
		out = append(out, c)
	}
	return out
}
