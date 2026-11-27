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
	"time"

	"github.com/hanzoai/cloud/claim"
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
type Window = claim.Window

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
	ords := make([]ord, len(facts))
	for i, f := range facts {
		ords[i] = ord{f}
	}
	w, losers, contested, ok := claim.Resolve(ords, asOf,
		func(a, b ord) bool { return a.Fact.Disposition == b.Fact.Disposition })
	if !ok {
		return Resolved{}, false
	}
	out := Resolved{
		Kind:      w.Fact.Kind,
		Subject:   w.Fact.Subject,
		At:        w.Fact.At,
		AsOf:      asOf,
		Winner:    w.Fact,
		Contested: contested,
	}
	for _, l := range losers {
		out.Conflicts = append(out.Conflicts, l.Fact)
	}
	return out, true
}

// ord adapts a Fact to the shared order. The four questions claim.Stronger
// asks are answered here and nowhere else; rank is the ONE term this plane
// contributes, and it is the fraud vocabulary's own adjudication weight.
//
// The methods shadow the promoted fields of the same name, which is the point:
// the field is the value, the method is the answer the order reads.
type ord struct{ Fact }

func (o ord) Knowable() time.Time { return o.Fact.Knowable }
func (o ord) Confidence() float64 { return o.Fact.Confidence }
func (o ord) Digest() string      { return o.Fact.ID }
func (o ord) Rank() int           { r, _ := rank(o.Fact.Source); return r }

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
