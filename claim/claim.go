// Package claim is the algebra of "somebody asserted something about a
// thing, at a moment, from evidence".
//
// It is a VALUE package: no I/O, no store, no clock, no vocabulary. Everything
// here is a pure function of its arguments, which is what lets the precedence
// rule and the leakage guard be tested exhaustively — and what lets two
// capabilities share them instead of each keeping a copy that drifts.
//
// It owns four things and deliberately nothing else:
//
//   - KNOWABLE, the derivation that makes the leakage guard a guard rather than
//     a courtesy (Knowable).
//   - DIGEST, the content address that makes redelivery idempotent (Digest).
//   - THE WINDOW an observation is made under (Window).
//   - THE TOTAL ORDER that resolves a conflict without discarding the losers
//     (Resolve).
//
// What it does NOT own is the vocabulary. A caller declares its own relations,
// its own values and its own ranking of sources, because those are claims about
// a domain and this package makes none. The ranking is the single parameter the
// order takes; every other term of it is general.
package claim

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"time"
)

// Skew is how far ahead of the server clock a caller's timestamp may sit before
// it is refused. Clocks differ by seconds; an assertion dated a year out would
// never mature and would sit in the store distorting coverage for a year, unseen.
const Skew = 5 * time.Minute

// Fact is what the order needs to know about an assertion, and no more. A caller
// keeps its own concrete type — with its own vocabulary, its own columns and its
// own store — and answers these four questions about it.
type Ordered interface {
	// Knowable is the instant THIS PLANE could first have answered with the
	// assertion. It is derived, never supplied: see Knowable.
	Knowable() time.Time
	// Confidence in [0,1]. A tie-breaker, never a substitute for rank.
	Confidence() float64
	// Digest is the content address, and the last tie-breaker. See Digest.
	Digest() string
	// Rank is the caller's adjudication weight for this assertion's source,
	// lowest first. It is the ONE domain-specific term of the order.
	Rank() int
}

// Knowable is the later of what the filer claims and what the server observed.
//
// WHY THE CLAIM CANNOT CARRY THE GUARD. `seen` is the filer's claim about the
// world's clock. A caller that files history with seen == at — the natural
// integration mistake — makes an assertion written today look knowable a year
// ago, and every horizon in the plane becomes decorative. A guard whose input is
// a caller-declared value is exactly as strong as the caller's honesty, which is
// not a guard.
//
// The later of the two is the honest instant and it is not a compromise: an
// assertion the plane did not hold could not have informed any decision the
// plane served, whatever the world knew. For a live pipeline the two are within
// minutes and this changes nothing. It bites exactly where it should — on
// history filed after the fact, visible from the moment it is filed and never
// before. `seen` is still recorded and returned; it is provenance, and it no
// longer decides anything.
func Knowable(seen, wrote time.Time) time.Time {
	if wrote.After(seen) {
		return wrote
	}
	return seen
}

// Digest is the content address of an assertion. Every semantic field is folded
// in length-prefixed, so no field's value can impersonate a field boundary and
// two distinct assertions cannot collide by concatenation.
//
// The caller passes its semantic fields and ONLY those. The server's clock, the
// store's position and any hold are deliberately excluded: including them would
// make every redelivery a new row and destroy idempotence, which is the property
// a webhook that retries depends on. A hold is a fact about the RECORD rather
// than about the world, so it is not part of what was asserted.
func Digest(parts ...string) string {
	h := sha256.New()
	for _, part := range parts {
		fmt.Fprintf(h, "%d:%s", len(part), part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Window is the observation a caller is resolving under, and it is the whole of
// the leakage guard.
//
// Now is the instant the caller is standing at — the materialisation instant, or
// a past instant for a backtest. Horizon is how long a thing must age before it
// may be admitted at all. Both are values on the request, not globals, because a
// backtest that could not move Now would resolve with today's knowledge and
// report a score no live decision could ever have earned.
type Window struct {
	Now     time.Time
	Horizon time.Duration
}

// Matured reports whether a thing at `at` has aged past the horizon.
func (w Window) Matured(at time.Time) bool { return !at.Add(w.Horizon).After(w.Now) }

// AsOf is the instant a thing at `at` observes its assertions at: the moment its
// horizon closes. An assertion knowable after it did not exist when a decision
// would have had to act, and it is invisible there.
func (w Window) AsOf(at time.Time) time.Time { return at.Add(w.Horizon) }

// Visible reports whether an assertion was knowable to this plane at an instant.
// It reads the derived Knowable and never a declared time.
func Visible(f Ordered, asOf time.Time) bool { return !f.Knowable().After(asOf) }

// Stronger is the TOTAL ORDER, and every term of it is deliberate:
//
//  1. RANK. Adjudication weight, declared once by the caller. A card network's
//     chargeback outranks an analyst's hunch because a different amount of
//     process stands behind it. This is the only term this package does not fix.
//  2. LATER Knowable. Within one rank, the most recently knowable claim wins —
//     which is exactly how a SOURCE CORRECTING ITSELF works: the correction is a
//     new assertion, it wins from the moment it became knowable, and the
//     original still wins for every observation instant BEFORE that. A
//     correction therefore cannot retroactively change what a past decision
//     knew. It is the DERIVED instant for the same reason Visible reads it: the
//     term deciding between two equally-ranked claims must not be one a caller
//     can set to any value it likes.
//  3. HIGHER Confidence. A stated confidence is only ever a tie-breaker; it
//     cannot lift a weak source above a strong one, or every caller would send 1.
//  4. LOWER Digest, so a tie is broken by a value both sides compute identically
//     rather than by whichever row the storage engine happened to return.
//
// A total order matters more than any single term of it: two runs over one set
// of assertions must produce one answer, or nothing downstream is comparable.
func Stronger(a, b Ordered) bool {
	if ra, rb := a.Rank(), b.Rank(); ra != rb {
		return ra < rb
	}
	if !a.Knowable().Equal(b.Knowable()) {
		return a.Knowable().After(b.Knowable())
	}
	if a.Confidence() != b.Confidence() {
		return a.Confidence() > b.Confidence()
	}
	return a.Digest() < b.Digest()
}

// Resolve is what is in force at an instant, and what disagreed with it.
//
// The losers are RETURNED rather than discarded: two sources that disagree both
// stay, and a contested set is visible as contested rather than resolved into
// silence. `agrees` decides contestation by comparing what two assertions
// CLAIM — the one comparison this package cannot make, because the claim is the
// caller's vocabulary.
//
// ok is false when nothing was knowable at asOf. That is not an error: it is the
// honest answer that this plane held nothing to say.
func Resolve[T Ordered](facts []T, asOf time.Time, agrees func(a, b T) bool) (winner T, conflicts []T, contested, ok bool) {
	seen := make([]T, 0, len(facts))
	for _, f := range facts {
		if Visible(f, asOf) {
			seen = append(seen, f)
		}
	}
	if len(seen) == 0 {
		return winner, nil, false, false
	}
	sort.SliceStable(seen, func(i, j int) bool { return Stronger(seen[i], seen[j]) })

	winner = seen[0]
	if len(seen) > 1 {
		conflicts = seen[1:]
	}
	for _, f := range conflicts {
		if !agrees(winner, f) {
			contested = true
			break
		}
	}
	return winner, conflicts, contested, true
}
