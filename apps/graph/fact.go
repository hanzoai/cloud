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
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/hanzoai/cloud/claim"
	"github.com/hanzoai/cloud/sdk/dialect"
	"golang.org/x/text/unicode/norm"
)

// Bounds on the three caller-sized values. Each is a store key or a sort term,
// not free text, and a count over caller-sized values is not a bound: with these
// asked at the entry point, `count × max` IS the byte bound of everything below.
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
	// Until is when the thing stopped being so, the caller's like At. Zero is
	// open: the statement holds until something ends it (see lives).
	Until time.Time
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
//
// Until joins the address only when it is set, so an open statement keeps the
// address it had before statements could end: a redelivery of one filed then is
// still one row.
func digest(f Fact) string {
	parts := []string{
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
	}
	if !f.Until.IsZero() {
		parts = append(parts, "until", strconv.FormatInt(f.Until.UTC().Unix(), 10))
	}
	return claim.Digest(parts...)
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

// when is a point in the plane's two times. valid is when the world was so —
// what At and Until speak of. known is how much this plane had heard by then —
// what Knowable speaks of. A read at a point answers "what was true at valid, as
// far as we knew at known": moving valid asks about another moment of the world,
// moving known asks what an earlier answer was, and neither disturbs the other.
type when struct{ valid, known time.Time }

// forever is a valid instant after every statement begins, so what holds there
// is what nothing has ended. The schema is read there: a declaration is about
// the organization's words, not about when the world was so.
var forever = time.Unix(1<<40, 0).UTC()

// versions is every STATEMENT about one pair as known at an instant, oldest
// first. A statement is (value, names, at) — what was said to hold, from when —
// and every assertion of it is a version.
//
// A version CORRECTS another only by being known later: the account learned
// last replaces every earlier account of the same statement, which is how a
// correction of when something ended replaces the first without deleting it.
// Accounts learned at the same instant correct nothing — two stints of one
// holder from the same year, filed together, are both kept, and what holds is
// decided among them by their own lifetimes and, for a pair holding one value,
// by the shared order.
func versions(facts []Fact, known time.Time) []Fact {
	type stated struct {
		value string
		names bool
		at    int64
	}
	latest := map[stated][]Fact{}
	for _, f := range facts {
		if f.Knowable.After(known) {
			continue
		}
		k := stated{f.Value, f.Names, f.At.Unix()}
		switch vs := latest[k]; {
		case len(vs) == 0 || f.Knowable.After(vs[0].Knowable):
			latest[k] = []Fact{f}
		case f.Knowable.Equal(vs[0].Knowable) && !slices.ContainsFunc(vs, func(v Fact) bool { return v.ID == f.ID }):
			latest[k] = append(vs, f)
		}
	}
	var out []Fact
	for _, vs := range latest {
		out = append(out, vs...)
	}
	slices.SortFunc(out, func(a, b Fact) int { return cmp.Or(a.At.Compare(b.At), strength(a, b)) })
	return out
}

// stronger is the shared order on two versions: later knowable, then higher
// confidence, then lower digest.
func stronger(a, b Fact) bool { return claim.Stronger(ord{a}, ord{b}) }

// strength compares two facts under the shared order, strongest first.
func strength(a, b Fact) int {
	switch {
	case stronger(a, b):
		return -1
	case stronger(b, a):
		return 1
	}
	return 0
}

// open reports whether a statement still holds at a valid instant by its own
// account: begun by then, and not yet at its Until.
func open(f Fact, valid time.Time) bool {
	return !f.At.After(valid) && (f.Until.IsZero() || valid.Before(f.Until))
}

// Resolve is what one (entity, relation) pair holds at a point — every statement
// in force, most recently begun first; the first is the winner — and, when the
// pair holds one value, the accounts of the winner's moment that disagree.
//
// A pair holds ONE value at a time or MANY, as its relation's cardinality says
// (schema.go): one for a property and many for an edge unless the organization
// declares otherwise.
//
// ONE: the value is the latest statement begun by the point, the strongest
// account of that moment winning. A later statement ends an earlier one, a
// retraction ends what it follows, and a statement past its own Until holds
// nothing — the pair does not fall back to what it had before. Weaker accounts
// of the same moment that name another value are conflicts: both stay visible,
// so a contested relation reads as contested rather than resolved into silence.
//
// MANY: each statement holds from At until its own Until, or until a retraction
// of the pair begins after it — or at the same moment, known later. An edge is
// not ended by another edge, which is what lets a position have five holders
// over five terms, or a service depend on three others at once.
func Resolve(facts []Fact, w when, one bool) (held, conflicts []Fact) {
	stated := versions(facts, w.known)
	if one {
		return single(stated, w.valid)
	}
	return multiple(stated, w.valid), nil
}

// single is Resolve for a pair holding one value at a time.
func single(stated []Fact, valid time.Time) (held, conflicts []Fact) {
	var top []Fact
	for _, f := range stated {
		switch {
		case f.At.After(valid):
		case len(top) == 0 || f.At.After(top[0].At):
			top = []Fact{f}
		case f.At.Equal(top[0].At):
			top = append(top, f)
		}
	}
	if len(top) == 0 {
		return nil, nil
	}
	slices.SortStableFunc(top, strength)
	win := top[0]
	if void(win) || !open(win, valid) {
		return nil, nil
	}
	for _, f := range top[1:] {
		if !void(f) && open(f, valid) && (f.Value != win.Value || f.Names != win.Names) &&
			!slices.ContainsFunc(conflicts, func(c Fact) bool { return c.Value == f.Value && c.Names == f.Names }) {
			conflicts = append(conflicts, f)
		}
	}
	return []Fact{win}, conflicts
}

// multiple is Resolve for a pair holding many values at once: every statement
// in force, each once. Two terms of one holder that overlap are two statements
// with their own evidence, and both hold. The retractions are indexed once, so
// a pair is resolved in n log n rather than one scan of them per statement.
func multiple(stated []Fact, valid time.Time) []Fact {
	var voids []Fact
	for _, f := range stated {
		if void(f) && !f.At.After(valid) {
			voids = append(voids, f)
		}
	}
	var out []Fact
	for _, f := range stated {
		if !void(f) && open(f, valid) && !retracted(voids, f) {
			out = append(out, f)
		}
	}
	slices.SortStableFunc(out, func(a, b Fact) int { return cmp.Or(b.At.Compare(a.At), strength(a, b)) })
	return out
}

// retracted reports whether any of voids — the pair's retractions begun by the
// instant asked about, oldest first — ends f: one begun after it, or one of its
// own moment known later.
func retracted(voids []Fact, f Fact) bool {
	i, _ := slices.BinarySearchFunc(voids, f.At, func(v Fact, t time.Time) int { return v.At.Compare(t) })
	for ; i < len(voids) && voids[i].At.Equal(f.At); i++ {
		if stronger(voids[i], f) {
			return true
		}
	}
	return i < len(voids)
}

// ender is the retraction that ends a statement of a many-valued pair by a
// valid instant — the first begun after it, or one of its own moment known
// later — or nil when none has.
func ender(stated []Fact, f Fact, valid time.Time) *Fact {
	for i := range stated {
		v := &stated[i]
		if !void(*v) || v.At.After(valid) {
			continue
		}
		if v.At.After(f.At) || (v.At.Equal(f.At) && stronger(*v, f)) {
			return v
		}
	}
	return nil
}

// edges is every edge in force at a point among facts spanning any number of
// pairs: each pair resolved on its own by the one rule, as many as its relation
// holds, and the edges it holds kept. A walk and a partition read the graph
// through this and nothing else, so the two cannot disagree about what the
// graph was.
func edges(facts []Fact, w when, card cardinality) []Fact {
	pairs := map[pair][]Fact{}
	for _, f := range facts {
		p := pair{f.Entity, f.Relation}
		pairs[p] = append(pairs[p], f)
	}
	var out []Fact
	for _, fs := range pairs {
		held, _ := Resolve(fs, w, card.one(fs))
		for _, f := range held {
			if f.Names {
				out = append(out, f)
			}
		}
	}
	return out
}

// void reports whether an assertion is a RETRACTION: a property with no value.
// It is a statement like any other: it ends what its pair held before it, and
// the statements it ends are still there to read, with the retraction beside
// them, which is the difference between withdrawn and gone.
func void(f Fact) bool { return !f.Names && f.Value == "" }

// delta is one change to a pair between two points.
type delta struct {
	kind     string
	was, now Fact
}

// changes is what became of one pair between two points, read off what it held
// at each and never off the rows alone: a version that loses the order changes
// nothing, and one that repeats what held is agreement, not change.
//
// "asserted" is a value held at b and not at a; "retracted" is one held at a and
// not at b, with what ended it as now — the retraction or later value, or the
// statement itself when its own Until did. A pair holding ONE value has its
// replacement reported once, as "superseded", rather than as an end and a start.
func changes(facts []Fact, a, b when, one bool) []delta {
	was, _ := Resolve(facts, a, one)
	now, _ := Resolve(facts, b, one)
	same := func(x, y Fact) bool { return x.Value == y.Value && x.Names == y.Names }
	if one && len(was) == 1 && len(now) == 1 {
		if same(was[0], now[0]) {
			return nil
		}
		return []delta{{kind: "superseded", was: was[0], now: now[0]}}
	}
	var out []delta
	for _, f := range now {
		if !slices.ContainsFunc(was, func(g Fact) bool { return same(f, g) }) {
			out = append(out, delta{kind: "asserted", now: f})
		}
	}
	stated := versions(facts, b.known)
	for _, f := range was {
		if slices.ContainsFunc(now, func(g Fact) bool { return same(f, g) }) {
			continue
		}
		d := delta{kind: "retracted", was: f, now: f}
		if e := endedBy(stated, f, b.valid, one); e != nil {
			d.now = *e
		}
		out = append(out, d)
	}
	return out
}

// endedBy is what ended a statement by a valid instant: for a one-valued pair
// the strongest account of the latest moment after it, for a many-valued pair
// its retraction; nil when its own Until did, or nothing has.
func endedBy(stated []Fact, f Fact, valid time.Time, one bool) *Fact {
	if !one {
		return ender(stated, f, valid)
	}
	var last *Fact
	for i := range stated {
		g := &stated[i]
		if g.At.After(valid) || g.ID == f.ID || g.At.Before(f.At) || (g.At.Equal(f.At) && !stronger(*g, f)) {
			continue
		}
		if last == nil || g.At.After(last.At) || (g.At.Equal(last.At) && stronger(*g, *last)) {
			last = g
		}
	}
	return last
}

// decay is how much certainty survives a chain of edges: the product of their
// confidences, and the index of the weakest, the first of them on a tie. ok is
// false for an empty chain and for any chain with an edge stating no confidence
// — absent reads as zero, and a product over a number nobody gave measures
// nothing.
func decay(edges []Fact) (product float64, weakest int, ok bool) {
	if len(edges) == 0 {
		return 0, 0, false
	}
	product = 1
	for i, e := range edges {
		if e.Confidence <= 0 {
			return 0, 0, false
		}
		product *= e.Confidence
		if e.Confidence < edges[weakest].Confidence {
			weakest = i
		}
	}
	return product, weakest, true
}

// key is an entity, a relation or an edge's value as this plane keeps it:
// Unicode NFC, every control character removed, the space around it trimmed.
// Admission folds every key to it and every read folds the key it is asked
// for, so "Café" typed composed and typed decomposed is one entity, one row and
// one content address. A scalar value is not a key and keeps its bytes: it is
// what somebody said, not a name anything is looked up by.
//
// A KEY FILED BEFORE ADMISSION FOLDED IS NOT REWRITTEN. The store is
// append-only, so a key it holds that was not already NFC — a heading copied
// out of a decomposing source, say — stays as it was filed, and a read, folding
// what it is asked for, does not find it under either spelling. A read does not
// also try the bytes it was sent: that would join the two spellings at that one
// read and nowhere else — the walk past it, the partition, every other pair's
// resolution would still hold them apart — so one read would answer as though
// they were one entity while the rest of the plane answered two. Such a key is
// still found by search and by a read that names no entity, walked into by the
// edges filed with it, and moved to its fold by asserting it again. Erasure is
// the exception (spellings).
func key(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return strings.TrimSpace(norm.NFC.String(s))
}

// folded is key over a list.
func folded(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = key(x)
	}
	return out
}

// spellings is every form a subject may have been filed under: its key; that
// key decomposed, which is how a key filed before admission folded was kept when
// its source decomposed; and the bytes sent, trimmed, which is how any other was
// kept. A read asks for the key alone (key). An erasure asks for all of them,
// because a subject left under a spelling nobody asked for is not erased.
func spellings(s string) []string {
	k := key(s)
	return slices.Compact(slices.Sorted(slices.Values([]string{k, norm.NFD.String(k), strings.TrimSpace(s)})))
}

// admit is the ONE entry point every caller-supplied assertion passes through, and
// every bound is asked here so no reader below has to ask again.
func admit(f Fact, now time.Time) (Fact, error) {
	var err error
	// The entity, the relation and an edge's value are KEYS — what every read
	// joins on and what the content address is taken over — so each is folded
	// before anything else is asked of it.
	f.Entity, f.Relation = key(f.Entity), key(f.Relation)
	if f.Names {
		f.Value = key(f.Value)
	}
	// An EDGE names another entity, so an empty value names nothing: the row
	// would claim a relation to a thing that cannot be walked to, and the walk
	// joining on it would follow it to nowhere. A PROPERTY may be empty, and that
	// is what a retraction is (void).
	if f.Names && f.Value == "" {
		return f, fmt.Errorf("an edge names an entity, so value is required")
	}
	f.Evidence = strings.TrimSpace(f.Evidence)
	if err = bounded("entity", f.Entity, entityMax); err != nil {
		return f, err
	}
	if err = bounded("relation", f.Relation, relationMax); err != nil {
		return f, err
	}
	if err = bounded("value", f.Value, valueMax); err != nil {
		return f, err
	}
	if err = bounded("evidence", f.Evidence, evidenceMax); err != nil {
		return f, err
	}
	if f.Names {
		if err = bounded("value", f.Value, entityMax); err != nil {
			return f, fmt.Errorf("an edge's value names an entity: %w", err)
		}
	}
	if f.Source = strings.TrimSpace(f.Source); f.Source == "" {
		return f, fmt.Errorf("source is required: an assertion nobody is named for cannot be weighed")
	}
	if f.Confidence < 0 || f.Confidence > 1 {
		return f, fmt.Errorf("confidence %v is outside [0,1]", f.Confidence)
	}
	// PostgreSQL refuses a NUL and bytes that are not UTF-8 in a column of text, and
	// SQLite keeps them: the same assertion is refused here, on both, for a reason
	// the caller can read.
	if err = dialect.Text(f.Entity, f.Relation, f.Value, f.Source, f.Evidence, f.By); err != nil {
		return f, fmt.Errorf("an assertion's text: %w", err)
	}
	// At is required at the wire, where absent and the first instant of the
	// calendar are two different things; here they are one value, so asking
	// IsZero would refuse 0001-01-01T00:00:00Z as though nothing had been sent.
	if !f.Until.IsZero() && !f.Until.After(f.At) {
		return f, fmt.Errorf("until must follow at: a statement cannot stop holding before it starts")
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

// bounded asks a field's bound of the bytes it will be kept as. It trims
// nothing: a key arrives folded, and a scalar is kept as sent.
func bounded(field, v string, max int) error {
	if v == "" && field != "evidence" && field != "value" {
		return fmt.Errorf("%s is required", field)
	}
	if len(v) > max {
		return fmt.Errorf("%s is %d bytes, over the %d-byte ceiling", field, len(v), max)
	}
	return nil
}
