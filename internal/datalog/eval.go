package datalog

// eval.go is the engine: each stratum to its fixpoint, semi-naively, over
// relations indexed on both arguments.

import (
	"cmp"
	"maps"
	"slices"
	"strings"
)

// Bound is the most one evaluation may do. Both are required: zero derives
// nothing and examines nothing.
type Bound struct {
	// Tuples is the most tuples it derives. Base tuples do not count.
	Tuples int
	// Steps is the most rows its joins examine — the work, which a rule can
	// spend without deriving anything new.
	Steps int
}

// Proof is how a tuple was first derived: the rule, by its index in the slice
// given to Compile, and the rule's positive body atoms as they matched, in the
// order they are written.
type Proof struct {
	Rule     int
	Supports []Ground
}

// Model is what an evaluation holds: every base tuple it was given for the
// predicates it read, every tuple it derived, and a proof for each derived one.
type Model struct {
	// Cut names the bound that stopped the evaluation, "tuples" or "steps", and
	// is empty when it reached its fixpoint. A cut model is sound — every tuple
	// in it holds — and incomplete: tuples that hold may be missing from it.
	Cut    string
	rels   map[string]*relation
	proofs map[Ground]Proof
}

// Holds reports whether a ground atom is in the model.
func (m *Model) Holds(g Ground) bool {
	r, ok := m.rels[g.Pred]
	return ok && r.set[g.Args]
}

// Tuples is every tuple of a predicate, base and derived, in the order they
// entered the model.
func (m *Model) Tuples(pred string) []Tuple {
	if r, ok := m.rels[pred]; ok {
		return slices.Clone(r.rows)
	}
	return nil
}

// Proof is how a tuple was derived. ok is false for a base tuple, and for one
// the model does not hold.
func (m *Model) Proof(g Ground) (Proof, bool) {
	p, ok := m.proofs[g]
	return p, ok
}

// relation is one predicate's tuples, in the order they entered, with a hash
// index on each argument. An index lists row positions ascending, so a range of
// rows — the relation before a round, or the tuples a round added — is a
// binary search into it.
type relation struct {
	rows []Tuple
	set  map[Tuple]bool
	idx  [2]map[string][]int
	// mark splits rows for the round being evaluated: before it is the relation
	// as it stood before the last round, from it the tuples that round added.
	mark int
}

func (r *relation) add(t Tuple) {
	if r.set[t] {
		return
	}
	n := len(r.rows)
	r.rows = append(r.rows, t)
	r.set[t] = true
	r.idx[0][t[0]] = append(r.idx[0][t[0]], n)
	r.idx[1][t[1]] = append(r.idx[1][t[1]], n)
}

// The three readings of a relation a body position takes in a round.
const (
	full  = iota // every tuple
	old          // the tuples before the last round's
	delta        // the last round's
)

func (r *relation) span(view int) (lo, hi int) {
	switch view {
	case old:
		return 0, r.mark
	case delta:
		return r.mark, len(r.rows)
	}
	return 0, len(r.rows)
}

// Eval evaluates what goals need of the program over base, stratum by stratum,
// within b. Base tuples of a unary predicate are projected onto their first
// argument. The result does not depend on the order base is given in.
func (p *Program) Eval(base map[string][]Tuple, goals []string, b Bound) *Model {
	need := p.cone(goals)
	m := &Model{rels: map[string]*relation{}, proofs: map[Ground]Proof{}}
	for _, q := range slices.Sorted(maps.Keys(need)) {
		r := &relation{set: map[Tuple]bool{}, idx: [2]map[string][]int{{}, {}}}
		ts := slices.Clone(base[q])
		if p.arity[q] == 1 {
			for i := range ts {
				ts[i][1] = ""
			}
		}
		slices.SortFunc(ts, func(a, b Tuple) int { return cmp.Or(strings.Compare(a[0], b[0]), strings.Compare(a[1], b[1])) })
		for _, t := range ts {
			r.add(t)
		}
		m.rels[q] = r
	}
	e := &evaluator{p: p, m: m, bound: b, fresh: map[Ground]bool{}}
	for _, s := range p.strata {
		// Every predicate of a component reaches every other, so one of them
		// being needed is all of them being needed.
		if !need[p.rules[s.rules[0]].head.pred] {
			continue
		}
		if e.stratum(s); m.Cut != "" {
			break
		}
	}
	return m
}

type evaluator struct {
	p       *Program
	m       *Model
	bound   Bound
	steps   int
	derived int
	// next is what this round derived, held apart until the round ends so that
	// every join in a round reads the same relations.
	next  []Ground
	fresh map[Ground]bool
}

// stratum runs one component to its fixpoint. A rule that reads the component
// is joined once per position that reads it, that position taking the delta; a
// rule that does not is joined once, in the first round, over relations that
// are already complete. The first round's delta is the component's base tuples.
func (e *evaluator) stratum(s stratum) {
	var once, each []*job
	for _, ri := range s.rules {
		recursive := false
		for k, a := range e.p.rules[ri].pos {
			if s.preds[a.pred] {
				recursive = true
				each = append(each, e.plan(ri, k, s.preds))
			}
		}
		if !recursive {
			once = append(once, e.plan(ri, -1, s.preds))
		}
	}
	for q := range s.preds {
		e.m.rels[q].mark = 0
	}
	for round := 0; ; round++ {
		jobs := each
		if round == 0 {
			jobs = slices.Concat(once, each)
		}
		for _, j := range jobs {
			if e.join(j, 0); e.m.Cut != "" {
				break
			}
		}
		for q := range s.preds {
			r := e.m.rels[q]
			r.mark = len(r.rows)
		}
		for _, g := range e.next {
			e.m.rels[g.Pred].add(g.Args)
		}
		n := len(e.next)
		e.next = e.next[:0]
		clear(e.fresh)
		if n == 0 || e.m.Cut != "" {
			return
		}
	}
}

// ── one rule, compiled for one choice of delta ───────────────────────────────

// ref is a term as a join reads it: a constant, or a variable's slot.
type ref struct {
	konst bool
	val   string
	slot  int
}

// How one argument of a positive atom meets a row.
const (
	isConst = iota // the row must hold the constant
	isBound        // the row must hold what the variable is already bound to
	binds          // the row binds the variable
)

type arg struct {
	kind int
	val  string
	slot int
}

// pattern is an atom as a join reads it.
type pattern struct {
	pred string
	args [2]ref
}

// check is a negated atom (pred set) or a builtin (pred empty, = when eq), run
// as soon as every variable it names is bound.
type check struct {
	pattern
	eq bool
}

// step is one positive atom in evaluation order.
type step struct {
	rel   *relation
	view  int
	args  [2]arg
	probe int // the column whose value is known before the row is read, or -1
	key   ref
	after []check
}

type job struct {
	rule  int
	steps []step
	pred  string
	head  [2]ref
	sup   []pattern
	env   []string
}

// plan compiles rule ri with position d taking the delta (-1: none). The delta
// is joined first, being the smallest reading; after it, each next atom is the
// first in written order that shares a bound variable, so the join probes an
// index rather than crossing two relations whenever the rule allows it.
func (e *evaluator) plan(ri, d int, in map[string]bool) *job {
	r := e.p.rules[ri]
	slot := map[string]int{}
	for _, a := range r.pos {
		for _, t := range a.args[:a.arity] {
			if _, ok := slot[t.name]; t.isVar && !ok {
				slot[t.name] = len(slot)
			}
		}
	}
	refOf := func(t term) ref {
		if !t.isVar {
			return ref{konst: true, val: t.name}
		}
		return ref{slot: slot[t.name]}
	}
	view := func(k int) int {
		switch {
		case d < 0 || !in[r.pos[k].pred]:
			return full
		case k < d:
			return old
		case k == d:
			return delta
		}
		return full
	}

	order := make([]int, 0, len(r.pos))
	used := make([]bool, len(r.pos))
	have := map[string]bool{}
	take := func(k int) {
		order = append(order, k)
		used[k] = true
		for _, t := range r.pos[k].args {
			if t.isVar {
				have[t.name] = true
			}
		}
	}
	if d >= 0 {
		take(d)
	}
	shares := func(t term) bool { return t.isVar && have[t.name] }
	for len(order) < len(r.pos) {
		pick := -1
		for k, a := range r.pos {
			if used[k] {
				continue
			}
			if pick < 0 {
				pick = k
			}
			if slices.ContainsFunc(a.args[:a.arity], shares) {
				pick = k
				break
			}
		}
		take(pick)
	}

	j := &job{rule: ri, pred: r.head.pred, env: make([]string, len(slot))}
	j.head = [2]ref{refOf(r.head.args[0]), refOf(r.head.args[1])}
	for _, a := range r.pos {
		j.sup = append(j.sup, pattern{a.pred, [2]ref{refOf(a.args[0]), refOf(a.args[1])}})
	}
	bound := map[string]int{} // variable → the step that binds it
	for s, k := range order {
		a := r.pos[k]
		st := step{rel: e.m.rels[a.pred], view: view(k), probe: -1}
		for c, t := range a.args {
			_, before := bound[t.name]
			switch {
			case !t.isVar:
				st.args[c] = arg{kind: isConst, val: t.name}
			case before:
				st.args[c] = arg{kind: isBound, slot: slot[t.name]}
			default:
				st.args[c] = arg{kind: binds, slot: slot[t.name]}
			}
			if st.probe < 0 && (!t.isVar || before) {
				st.probe, st.key = c, refOf(t)
			}
		}
		for _, t := range a.args {
			if _, ok := bound[t.name]; t.isVar && !ok {
				bound[t.name] = s
			}
		}
		// p(X, X): the second X meets the first within the same row.
		if a.args[0].isVar && a.args[1].isVar && a.args[0].name == a.args[1].name && st.args[0].kind == binds {
			st.args[1].kind = isBound
		}
		j.steps = append(j.steps, st)
	}
	schedule := func(c check, terms ...term) {
		at := 0
		for _, t := range terms {
			if t.isVar {
				at = max(at, bound[t.name])
			}
		}
		j.steps[at].after = append(j.steps[at].after, c)
	}
	for _, a := range r.neg {
		schedule(check{pattern: pattern{a.pred, [2]ref{refOf(a.args[0]), refOf(a.args[1])}}}, a.args[:a.arity]...)
	}
	for _, c := range r.tests {
		schedule(check{pattern: pattern{args: [2]ref{refOf(c.l), refOf(c.r)}}, eq: c.eq}, c.l, c.r)
	}
	return j
}

func (j *job) val(r ref) string {
	if r.konst {
		return r.val
	}
	return j.env[r.slot]
}

// join matches steps[s:] against the model. It stops once a bound cuts.
func (e *evaluator) join(j *job, s int) {
	if s == len(j.steps) {
		e.derive(j)
		return
	}
	st := &j.steps[s]
	lo, hi := st.rel.span(st.view)
	if st.probe < 0 {
		for i := lo; i < hi; i++ {
			if !e.try(j, s, st.rel.rows[i]) {
				return
			}
		}
		return
	}
	at := st.rel.idx[st.probe][j.val(st.key)]
	from, _ := slices.BinarySearch(at, lo)
	for _, i := range at[from:] {
		if i >= hi || !e.try(j, s, st.rel.rows[i]) {
			return
		}
	}
}

// try matches one row at step s and joins onward, reporting false once a bound
// has cut the evaluation.
func (e *evaluator) try(j *job, s int, t Tuple) bool {
	if e.steps++; e.steps > e.bound.Steps {
		e.m.Cut = "steps"
		return false
	}
	st := &j.steps[s]
	for c, a := range st.args {
		switch a.kind {
		case isConst:
			if t[c] != a.val {
				return true
			}
		case isBound:
			if t[c] != j.env[a.slot] {
				return true
			}
		case binds:
			j.env[a.slot] = t[c]
		}
	}
	for _, c := range st.after {
		l, r := j.val(c.args[0]), j.val(c.args[1])
		if c.pred == "" && (l == r) != c.eq || c.pred != "" && e.m.rels[c.pred].set[Tuple{l, r}] {
			return true
		}
	}
	e.join(j, s+1)
	return e.m.Cut == ""
}

// derive records the head of a complete match, with its proof, unless the model
// or this round already holds it.
func (e *evaluator) derive(j *job) {
	g := Ground{Pred: j.pred, Args: Tuple{j.val(j.head[0]), j.val(j.head[1])}}
	if e.m.rels[g.Pred].set[g.Args] || e.fresh[g] {
		return
	}
	if e.derived == e.bound.Tuples {
		e.m.Cut = "tuples"
		return
	}
	e.derived++
	e.fresh[g] = true
	e.next = append(e.next, g)
	sup := make([]Ground, len(j.sup))
	for k, a := range j.sup {
		sup[k] = Ground{Pred: a.pred, Args: Tuple{j.val(a.args[0]), j.val(a.args[1])}}
	}
	e.m.proofs[g] = Proof{Rule: j.rule, Supports: sup}
}
