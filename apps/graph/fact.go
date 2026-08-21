package graph

// fact.go is the VALUE. No I/O, no store, no clock: everything here is a pure
// function of its arguments.
//
// A node is NOT a row. An entity exists because something was asserted about it,
// so there is no node table, no create and no cascade delete — nothing to keep in
// step with anything. An EDGE is an assertion whose value names another entity; a
// PROPERTY is an assertion whose value is a scalar. They are one thing, stored
// once, and `names` is the one bit that tells them apart.
//
// The algebra — the derived knowable instant, the content address, the window and
// the total order — is `claim`, shared with the ground-truth plane. This file
// contributes the vocabulary and the bounds, which is all a caller of that
// algebra is meant to contribute.

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/claim"
)

// Bounds on the three caller-sized values. Each is a store key or a sort term,
// not free text, and a count over caller-sized values is not a bound: with these
// asked at the door, `count × max` IS the byte bound of everything below.
const (
	entityMax   = 512
	relationMax = 128
	valueMax    = 2048
	evidenceMax = 512
)

// Fact is one assertion: somebody, at some moment, from some evidence, asserted
// that an entity stands in a relation to a value.
type Fact struct {
	// Seq is the store's own position, assigned inside the inserting statement.
	Seq int64
	// ID is the content address of the assertion (claim.Digest). Redelivery of
	// the same assertion is one row; anything differing in any field is a
	// DIFFERENT assertion and is recorded as one. Nothing is ever overwritten.
	ID       string
	Entity   string
	Relation string
	Value    string
	// Names is true when Value is another entity's key — the assertion is an
	// edge. When false the assertion is a property of Entity. A walk reads only
	// the edges, which is why this is a column and not a guess about the value.
	Names bool
	// At is when the thing was so; Seen is when the filer says it became
	// knowable. Both are the caller's.
	At   time.Time
	Seen time.Time
	// Source names who asserted. It is open here — this plane adjudicates
	// between no two publishers — which is why Rank is constant below.
	Source string
	// Evidence points at the record this claim came from. An assertion nobody
	// can defend is not evidence, so it is required.
	Evidence string
	// By is the identity that asserted, stamped server-side from the validated
	// principal and never taken from the body: an attributable record whose
	// attribution the caller chose is not attributable.
	By string
	// Confidence in [0,1]; a tie-breaker in the order, never a substitute for it.
	Confidence float64
	// Hold marks a litigation hold. It is a fact about the RECORD and not about
	// the world, so it is outside the digest and moves only by its own op.
	Hold bool
	// Wrote is the server clock at the write — the only time a tenant cannot
	// move, and what a retention sweep measures against.
	Wrote time.Time
	// Knowable is the instant this plane could first have answered with the
	// assertion: claim.Knowable(Seen, Wrote), derived and never supplied.
	Knowable time.Time
}

// digest is the content address. Wrote, Knowable, Seq and Hold are deliberately
// excluded — see claim.Digest.
func digest(f Fact) string {
	return claim.Digest(
		f.Entity,
		f.Relation,
		f.Value,
		strconv.FormatBool(f.Names),
		strconv.FormatInt(f.At.UTC().Unix(), 10),
		strconv.FormatInt(f.Seen.UTC().Unix(), 10),
		f.Source,
		f.Evidence,
		f.By,
		strconv.FormatFloat(f.Confidence, 'f', 6, 64),
	)
}

// ord adapts a Fact to the shared order. The methods shadow the promoted fields
// of the same name: the field is the value, the method is the answer the order
// reads.
//
// Rank is CONSTANT, and that is a claim this plane makes deliberately. Rank is
// adjudication weight — the assertion that one publisher outranks another — and
// this plane holds no such vocabulary, so it asserts none. The order therefore
// falls through to its remaining terms: later knowable, higher confidence, lower
// digest. It stays total, and it stays reproducible.
type ord struct{ Fact }

func (o ord) Knowable() time.Time { return o.Fact.Knowable }
func (o ord) Confidence() float64 { return o.Fact.Confidence }
func (o ord) Digest() string      { return o.Fact.ID }
func (o ord) Rank() int           { return 0 }

// Resolve is what is in force about one (entity, relation) at an instant, and
// what disagreed. Two sources that disagree BOTH stay: a contested relation is
// visible as contested rather than resolved into silence.
func Resolve(facts []Fact, asOf time.Time) (winner Fact, conflicts []Fact, contested, ok bool) {
	ords := make([]ord, len(facts))
	for i, f := range facts {
		ords[i] = ord{f}
	}
	w, losers, contested, ok := claim.Resolve(ords, asOf,
		func(a, b ord) bool { return a.Fact.Value == b.Fact.Value })
	if !ok {
		return Fact{}, nil, false, false
	}
	for _, l := range losers {
		conflicts = append(conflicts, l.Fact)
	}
	return w.Fact, conflicts, contested, true
}

// admit is the ONE door every caller-supplied assertion passes, and every bound
// is asked here so no reader below has to ask again.
func admit(f Fact, now time.Time) (Fact, error) {
	var err error
	if f.Entity, err = bounded("entity", f.Entity, entityMax); err != nil {
		return f, err
	}
	if f.Relation, err = bounded("relation", f.Relation, relationMax); err != nil {
		return f, err
	}
	if f.Value, err = bounded("value", f.Value, valueMax); err != nil {
		return f, err
	}
	if f.Evidence, err = bounded("evidence", f.Evidence, evidenceMax); err != nil {
		return f, err
	}
	if f.Names {
		if _, err = bounded("value", f.Value, entityMax); err != nil {
			return f, fmt.Errorf("an edge's value names an entity: %w", err)
		}
	}
	if f.Source = strings.TrimSpace(f.Source); f.Source == "" {
		return f, fmt.Errorf("source is required: an assertion nobody is named for cannot be weighed")
	}
	if f.Confidence < 0 || f.Confidence > 1 {
		return f, fmt.Errorf("confidence %v is outside [0,1]", f.Confidence)
	}
	if f.At.IsZero() {
		return f, fmt.Errorf("at is required: an assertion with no instant can never mature")
	}
	if f.Seen.IsZero() {
		f.Seen = f.At
	}
	if f.Seen.Before(f.At) {
		return f, fmt.Errorf("seen precedes at: a claim cannot be knowable before it was so")
	}
	// A timestamp past the skew bound would never mature and would sit in the
	// store distorting every read for as long as it is ahead.
	if f.At.After(now.Add(claim.Skew)) || f.Seen.After(now.Add(claim.Skew)) {
		return f, fmt.Errorf("timestamp is more than %v ahead of the server clock", claim.Skew)
	}
	return f, nil
}

func bounded(field, v string, max int) (string, error) {
	v = strings.TrimSpace(v)
	if v == "" && field != "evidence" && field != "value" {
		return v, fmt.Errorf("%s is required", field)
	}
	if len(v) > max {
		return v, fmt.Errorf("%s is %d bytes, over the %d-byte ceiling", field, len(v), max)
	}
	return v, nil
}
