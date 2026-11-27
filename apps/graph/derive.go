package graph

// derive.go is what the organization's rules conclude from its assertions: one
// Datalog engine (internal/datalog) over the graph as it stood at a point.
//
// RULES ARE ASSERTIONS, like the schema. A rule is (`rule:<name>`, `rule`,
// `<text>`) — one rule per name, its text the value:
//
//	("rule:ancestor-base", "rule", "ancestor(X, Y) :- parent(X, Y)")
//	("rule:ancestor-step", "rule", "ancestor(X, Z) :- parent(X, Y), ancestor(Y, Z)")
//	("rule:safe",          "rule", "safe(X) :- node(X), not bad(X)")
//
// So a rule has a source, evidence, an asserter and a knowable instant, a
// rewrite is a later assertion of the same name, a withdrawal is its retraction,
// and "what did the rules say last Tuesday" is an as-known read. They are read
// as the schema is, at forever as known at the point: a rule is about the
// organization's words, not about when the world was so.
//
// ADMITTED ONLY IF EVALUABLE. A rule is refused at assertion unless it parses,
// is safe, takes no until, and leaves the rules in force stratifiable — at the
// instant it becomes knowable and at every later instant a rule already filed
// becomes knowable — judged in the transaction that files it (write), so no
// point in time holds a rule set this file cannot evaluate. A row whose text is
// no rule, filed under a `rule:` key before rules were admitted, enacts nothing.
// A rule decides what every derivation concludes, so it is a declaration
// (schematic): only an admin of the organization files one, and each is on the
// audit trail.
//
// THE BASE IS THE GRAPH AS RESOLVE READS IT. A predicate's base tuples are the
// statements of the relation of that name in force at the point — each pair
// resolved by the one rule, so a retraction, an until and a declared cardinality
// all apply — edges and properties alike: (X, p, Y) is the tuple (X, Y). A
// predicate the rules use with one argument is "X is p": any statement in force
// of (X, p, ·). The read is bounded by the ceiling a partition is.
//
// NOTHING DERIVED IS STORED unless an admin asks. A conclusion is recomputed from
// the assertions on every call, with its proof: the rule, and each support as
// the assertion it rests on or the derived atom it is — so an answer can never
// be staler than the rows it came from, and every ID it cites is a row in force
// at the point. Asked to file, an admin's derivation of the graph as it stands
// now is filed as assertions, one write on the audit trail (enter).

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/internal/datalog"
	"github.com/hanzoai/cloud/sdk/dialect"
	"github.com/zap-proto/zip"
)

const (
	// ruleKey is the relation a rule's text is asserted under, of an entity
	// named rulePrefix + the rule's name.
	ruleKey    = "rule"
	rulePrefix = ruleKey + ":"
	// deriveMax is the most tuples one evaluation derives. Past it the answer is
	// what was derived by then — every tuple of it true — and says it was cut.
	deriveMax = walkBound
	// stepMax is the most rows one evaluation's joins examine: the work, which
	// a rule can spend without deriving anything new.
	stepMax = 10_000_000
	// proofMax is the most atoms one conclusion's proof is walked through to
	// find the assertions it rests on. A larger proof carries no decay.
	proofMax = 1024
)

// enacts reports whether an assertion states a rule.
func enacts(f Fact) bool { return f.Relation == ruleKey && strings.HasPrefix(f.Entity, rulePrefix) }

// laws is every rule assertion as known at an instant, every version and
// retraction included; zero reads everything filed. cut says there are more
// than one read returns.
func (s *store) laws(ctx context.Context, known time.Time) ([]Fact, bool, error) {
	// A range and not LIKE: LIKE folds ASCII case, and `RULE:x` is not a rule.
	q := `SELECT ` + cols + ` FROM assertion WHERE relation = $1 AND entity >= $2 AND entity < $3`
	args := []any{ruleKey, rulePrefix, ruleKey + ";"}
	if !known.IsZero() {
		args = append(args, known.Unix())
		q += ` AND knowable <= ` + mark(args)
	}
	lim, args := bound(args, walkBound+1)
	facts, err := s.query(ctx, q+` ORDER BY seq`+lim, args...)
	if err != nil {
		return nil, false, err
	}
	if len(facts) > walkBound {
		return nil, true, nil
	}
	return facts, false, nil
}

// law is one rule in force: the assertion that states it, and the rule.
type law struct {
	Fact
	rule datalog.Rule
}

// statute is the rules in force as known at an instant, by name: each rule's
// pair resolved at forever, so a later text replaces an earlier one and a
// retraction withdraws it. A text that is no rule enacts nothing.
func statute(rows []Fact, known time.Time) []law {
	by := map[string][]Fact{}
	for _, f := range rows {
		by[f.Entity] = append(by[f.Entity], f)
	}
	var out []law
	for _, name := range slices.Sorted(maps.Keys(by)) {
		held, _ := Resolve(by[name], when{valid: forever, known: known}, true)
		if len(held) == 0 {
			continue
		}
		if r, err := datalog.Parse(held[0].Value); err == nil {
			out = append(out, law{Fact: held[0], rule: r})
		}
	}
	return out
}

func compile(laws []law) (*datalog.Program, error) {
	rules := make([]datalog.Rule, len(laws))
	for i, l := range laws {
		rules[i] = l.rule
	}
	return datalog.Compile(rules)
}

// lawful refuses, into why, each rule assertion that would leave the rules in
// force unable to be evaluated. The batch's own rules count in the order sent,
// so a document that states two rules at once is judged as the set they make.
// A retraction is always admitted: fewer rules stratify whenever more did.
func (s *store) lawful(ctx context.Context, facts []Fact, why []string) error {
	var rows []Fact
	read, over := false, false
	for i, f := range facts {
		if why[i] != "" || !enacts(f) || void(f) {
			continue
		}
		if !read {
			stored, cut, err := s.laws(ctx, time.Time{})
			if err != nil {
				return err
			}
			rows, read, over = stored, true, cut
		}
		if over {
			why[i] = fmt.Sprintf("%s: this organization holds more than %d rule assertions, the most one derivation reads", f.Entity, walkBound)
			continue
		}
		if why[i] = admissible(rows, f); why[i] == "" {
			rows = append(rows, f)
		}
	}
	return nil
}

// admissible is why a rule assertion is refused against the rows already
// filed, or "".
func admissible(rows []Fact, f Fact) string {
	switch {
	case f.Entity == rulePrefix:
		return f.Entity + ": a rule is named, rule:<name>"
	case f.Names:
		return fmt.Sprintf("%s rule %s: a rule's value is its text, not an entity", f.Entity, f.Value)
	case !f.Until.IsZero():
		return f.Entity + ": a rule is in force until it is rewritten or withdrawn, so it takes no until"
	}
	refuse := func(err error) string { return fmt.Sprintf("%s rule %q: %v", f.Entity, f.Value, err) }
	if _, err := datalog.Parse(f.Value); err != nil {
		return refuse(err)
	}
	with := append(slices.Clone(rows), f)
	at := []time.Time{f.Knowable}
	for _, r := range rows {
		if r.Knowable.After(f.Knowable) {
			at = append(at, r.Knowable)
		}
	}
	for _, known := range at {
		if _, err := compile(statute(with, known)); err != nil {
			return refuse(err)
		}
	}
	return ""
}

// grounds is every assertion of the relations named, begun and known by the
// point — edges, properties and the retractions that end them. ok is false,
// not a short read, when there are more than edgeMax: a conclusion drawn from
// part of the graph is wrong about the part it dropped, and a negation over it
// concludes what is false.
func (s *store) grounds(ctx context.Context, relations []string, w when) ([]Fact, bool, error) {
	var out []Fact
	for chunk := range slices.Chunk(relations, seedMax) {
		q := `SELECT ` + cols + ` FROM assertion WHERE relation IN (` + dialect.Marks(1, len(chunk)) + `) AND at <= $` + strconv.Itoa(len(chunk)+1) + ` AND knowable <= $` + strconv.Itoa(len(chunk)+2) + ` ORDER BY seq LIMIT $` + strconv.Itoa(len(chunk)+3)
		facts, err := s.query(ctx, q, append(anys(chunk), w.valid.Unix(), w.known.Unix(), edgeMax+1-len(out))...)
		if err != nil {
			return nil, false, err
		}
		if out = append(out, facts...); len(out) > edgeMax {
			return nil, false, nil
		}
	}
	return out, true, nil
}

// holding is every statement in force at a point among facts spanning any
// number of pairs: what edges keeps, with the properties kept too.
func holding(facts []Fact, w when, card cardinality) []Fact {
	pairs := map[pair][]Fact{}
	for _, f := range facts {
		p := pair{f.Entity, f.Relation}
		pairs[p] = append(pairs[p], f)
	}
	var out []Fact
	for _, fs := range pairs {
		held, _ := Resolve(fs, w, card.one(fs))
		out = append(out, held...)
	}
	return out
}

// derivation is one evaluation and what its base atoms rest on.
type derivation struct {
	laws  []law
	model *datalog.Model
	// rests is the assertion each base atom rests on: of the statements in
	// force that state it, the one Resolve ranks first.
	rests map[datalog.Ground]Fact
	spans map[datalog.Ground][2]time.Time
	named map[column]bool
}

// column is one argument of a ground atom.
type column struct {
	g   datalog.Ground
	col int
}

// evaluate derives goals from the facts in force at w under prog, the program
// compiled from laws. A predicate prog names nowhere takes the arity shape gives
// it, so a goal a what-if leaves no rule of keeps the shape the question asks
// it in.
func evaluate(prog *datalog.Program, laws []law, facts []Fact, w when, card cardinality, goals []string, shape *datalog.Program) *derivation {
	d := &derivation{laws: laws, rests: map[datalog.Ground]Fact{}, spans: map[datalog.Ground][2]time.Time{}, named: map[column]bool{}}
	base := map[string][]datalog.Tuple{}
	for _, f := range holding(facts, w, card) {
		t := datalog.Tuple{f.Entity, f.Value}
		if cmp.Or(prog.Arity(f.Relation), shape.Arity(f.Relation)) == 1 {
			t[1] = ""
		}
		g := datalog.Ground{Pred: f.Relation, Args: t}
		if _, ok := d.rests[g]; !ok {
			d.rests[g] = f
			base[f.Relation] = append(base[f.Relation], t)
		}
	}
	d.model = prog.Eval(base, goals, datalog.Bound{Tuples: deriveMax, Steps: stepMax})
	return d
}

// span is the interval the assertions a conclusion's proof rests on share: from
// the latest start among them to the earliest end any of them states, zero for
// open.
func (d *derivation) span(g datalog.Ground) (from, until time.Time) {
	if s, ok := d.spans[g]; ok {
		return s[0], s[1]
	}
	if f, ok := d.rests[g]; ok {
		from, until = f.At, f.Until
	} else {
		pr, _ := d.model.Proof(g)
		for i, sup := range pr.Supports {
			a, b := d.span(sup)
			if i == 0 || a.After(from) {
				from = a
			}
			if !b.IsZero() && (until.IsZero() || b.Before(until)) {
				until = b
			}
		}
	}
	d.spans[g] = [2]time.Time{from, until}
	return from, until
}

// names reports whether argument col of an atom is an entity, by its proof: a
// statement's subject is, and its value when it is an edge; a derived atom's
// argument is what the atom its rule read it from says of that column
// (datalog.Rule.Origin), and a rule's constant is a scalar.
func (d *derivation) names(g datalog.Ground, col int) bool {
	if f, ok := d.rests[g]; ok {
		return col == 0 || f.Names
	}
	k := column{g, col}
	if v, ok := d.named[k]; ok {
		return v
	}
	pr, _ := d.model.Proof(g)
	at, c, ok := d.laws[pr.Rule].rule.Origin(col)
	d.named[k] = ok && d.names(pr.Supports[at], c)
	return d.named[k]
}

// leaves is the distinct assertions a conclusion's proof rests on, in the order
// the proof first reaches them. ok is false past proofMax atoms.
func (d *derivation) leaves(g datalog.Ground) (out []Fact, ok bool) {
	seen := map[datalog.Ground]bool{}
	var walk func(g datalog.Ground) bool
	walk = func(g datalog.Ground) bool {
		if seen[g] {
			return true
		}
		if len(seen) == proofMax {
			return false
		}
		seen[g] = true
		if f, ok := d.rests[g]; ok {
			out = append(out, f)
			return true
		}
		pr, _ := d.model.Proof(g)
		for _, s := range pr.Supports {
			if !walk(s) {
				return false
			}
		}
		return true
	}
	return out, walk(g)
}

func (d *derivation) conclusion(g datalog.Ground) graphConclusion {
	pr, _ := d.model.Proof(g)
	rule := d.laws[pr.Rule]
	from, until := d.span(g)
	c := graphConclusion{
		Predicate: g.Pred, Subject: g.Args[0], Object: g.Args[1],
		Rule: rule.Entity, RuleID: rule.ID, Supports: make([]graphSupport, 0, len(pr.Supports)),
		From: from.Format(time.RFC3339), Until: stamp(until),
	}
	for _, s := range pr.Supports {
		sup := graphSupport{Predicate: s.Pred, Subject: s.Args[0], Object: s.Args[1]}
		if f, ok := d.rests[s]; ok {
			sup.Assertion = f.ID
		}
		c.Supports = append(c.Supports, sup)
	}
	if leaves, ok := d.leaves(g); ok {
		if product, weakest, ok := decay(leaves); ok {
			c.Decay, c.Weakest = product, leaves[weakest].ID
		}
	}
	return c
}

// answer is every derived tuple of the goals, and every derived atom outside
// them their proofs cite — so each support that is not an assertion is an entry
// of one list or the other.
func (d *derivation) answer(goals []string) (derived, lemmas []graphConclusion) {
	derived, lemmas = []graphConclusion{}, []graphConclusion{}
	asked := map[string]bool{}
	for _, q := range goals {
		asked[q] = true
	}
	var mine []datalog.Ground
	for _, q := range goals {
		for _, t := range d.model.Tuples(q) {
			if g := (datalog.Ground{Pred: q, Args: t}); !d.base(g) {
				mine = append(mine, g)
			}
		}
	}
	cited := map[datalog.Ground]bool{}
	var cite func(g datalog.Ground)
	cite = func(g datalog.Ground) {
		pr, _ := d.model.Proof(g)
		for _, s := range pr.Supports {
			if d.base(s) || asked[s.Pred] || cited[s] {
				continue
			}
			cited[s] = true
			cite(s)
		}
	}
	for _, g := range mine {
		cite(g)
		derived = append(derived, d.conclusion(g))
	}
	for g := range cited {
		lemmas = append(lemmas, d.conclusion(g))
	}
	order := func(a, b graphConclusion) int {
		return cmp.Or(cmp.Compare(a.Predicate, b.Predicate), cmp.Compare(a.Subject, b.Subject), cmp.Compare(a.Object, b.Object))
	}
	slices.SortFunc(derived, order)
	slices.SortFunc(lemmas, order)
	return derived, lemmas
}

func (d *derivation) base(g datalog.Ground) bool {
	_, ok := d.model.Proof(g)
	return !ok
}

// clash is why filing the goals' conclusions would leave a pair holding less than
// the rules conclude of it, or "": a relation that holds one value at a time
// keeps one of two values concluded of a subject, or of one concluded beside
// one asserted, and which one would be the store's choice, not the rules'.
func (d *derivation) clash(goals []string, card cardinality) string {
	for _, q := range goals {
		by, derived := map[string][]Fact{}, map[string]bool{}
		for _, t := range d.model.Tuples(q) {
			g := datalog.Ground{Pred: q, Args: t}
			by[t[0]] = append(by[t[0]], Fact{Relation: q, Value: t[1], Names: d.names(g, 1)})
			derived[t[0]] = derived[t[0]] || !d.base(g)
		}
		for _, subject := range slices.Sorted(maps.Keys(by)) {
			if held := by[subject]; derived[subject] && len(held) > 1 && card.one(held) {
				values := make([]string, len(held))
				for i, f := range held {
					values[i] = fmt.Sprintf("%q", f.Value)
				}
				slices.Sort(values)
				return fmt.Sprintf("%s holds one value at a time, and the rules conclude %s has %s: filed, only one would hold. "+
					"Declare (relation:%s, cardinality, many) to file them all", q, subject, strings.Join(values, " and "), q)
			}
		}
	}
	return ""
}

// holds is every tuple of the goals, asserted or derived.
func (d *derivation) holds(goals []string) map[datalog.Ground]bool {
	out := map[datalog.Ground]bool{}
	for _, q := range goals {
		for _, t := range d.model.Tuples(q) {
			out[datalog.Ground{Pred: q, Args: t}] = true
		}
	}
	return out
}

// ── the wire ─────────────────────────────────────────────────────────────────

type graphDeriveIn struct {
	// Predicates are what to derive: every tuple the rules in force conclude of
	// each, with its proof. Each must be the head of a rule in force, asserted
	// as (`rule:<name>`, `rule`, `<text>`) — for example `ancestor(X, Z) :-
	// parent(X, Y), ancestor(Y, Z)`. At least one, at most 256.
	Predicates []string `json:"predicates"`
	// AsOf derives from the graph as it stood at an instant of the world, RFC
	// 3339: the statements that held then. Absent is now.
	AsOf string `json:"as_of,omitempty"`
	// AsKnown is how much this plane had heard, RFC 3339 — of the statements
	// and of the rules — so a past answer is reproducible. Absent is now.
	AsKnown string `json:"as_known,omitempty"`
	// Without is assertion IDs, of statements or of rules, to derive without as
	// though they had never been filed. The derivation then runs twice, and
	// Lost and Gained say what that changes. Each must be a row this derivation
	// reads. At most 256.
	Without []string `json:"without,omitempty"`
	// File files each tuple Derived lists as an assertion: (subject, predicate,
	// object) from the instant it was derived at, open, its source the rule,
	// its evidence the rule's ID, its confidence the decay, and an edge when
	// the object is an entity by its proof. It requires an admin of the
	// organization, the graph as it stands now — no as_of, as_known or
	// without — and predicates with two arguments. It is refused whole when a
	// relation that holds one value at a time would keep only one of the
	// values a subject is concluded or asserted to have, and when a conclusion
	// is about a `relation:` or `rule:` key. Each is judged as any assertion
	// is, and the write is on the audit trail before it commits. A filed
	// conclusion is an assertion like any other: it does not fall when a
	// support later does.
	File bool `json:"file,omitempty"`
}

// graphAtom is one tuple of a predicate.
type graphAtom struct {
	// Predicate is the relation the tuple is of.
	Predicate string `json:"predicate"`
	// Subject is its first argument.
	Subject string `json:"subject"`
	// Object is its second argument. Absent for a predicate the rules use with
	// one argument.
	Object string `json:"object,omitempty"`
}

// graphSupport is one atom a conclusion's rule matched.
type graphSupport struct {
	// Predicate is the relation the supporting atom is of.
	Predicate string `json:"predicate"`
	// Subject is its first argument.
	Subject string `json:"subject"`
	// Object is its second argument. Absent for a predicate the rules use with
	// one argument.
	Object string `json:"object,omitempty"`
	// Assertion is the ID of the statement in force the atom rests on. Absent
	// when the atom is itself derived: it is then an entry of Derived or
	// Lemmas, with a proof of its own.
	Assertion string `json:"assertion,omitempty"`
}

// graphConclusion is one derived tuple and its proof.
type graphConclusion struct {
	// Predicate is the relation concluded.
	Predicate string `json:"predicate"`
	// Subject is the tuple's first argument.
	Subject string `json:"subject"`
	// Object is its second argument. Absent for a predicate the rules use with
	// one argument.
	Object string `json:"object,omitempty"`
	// Rule is the rule that concluded it, `rule:<name>`.
	Rule string `json:"rule"`
	// RuleID is the ID of the assertion stating that rule's text as it was in
	// force.
	RuleID string `json:"rule_id"`
	// Supports are the rule's positive atoms as they matched, in the order the
	// rule writes them. What the rule negates is absent at the point and has no
	// support to cite.
	Supports []graphSupport `json:"supports"`
	// From is the latest start among the statements the proof rests on, RFC
	// 3339. What a rule negates has no statement to date it, so a conclusion
	// drawn through a negation may not have held from then.
	From string `json:"from"`
	// Until is the earliest until any of those statements states, RFC 3339.
	// Absent is open. A statement a later assertion ends is a later question,
	// asked at a later as_of.
	Until string `json:"until,omitempty"`
	// Decay is the product of the confidences of the distinct statements the
	// proof rests on: how much certainty survives the derivation. Absent unless
	// every one states a confidence above zero and the proof has at most 1024
	// atoms.
	Decay float64 `json:"decay,omitempty"`
	// Weakest is the ID of the statement with the lowest confidence among them,
	// the first on a tie: the link the conclusion is only as strong as. Absent
	// exactly when Decay is.
	Weakest string `json:"weakest,omitempty"`
}

type graphDeriveOut struct {
	// AsOf is the instant the graph was read at, RFC 3339: the one asked for, or
	// the server's clock when none was.
	AsOf string `json:"as_of"`
	// AsKnown is the knowledge instant it was taken at, RFC 3339, the same way.
	AsKnown string `json:"as_known"`
	// Derived is every tuple of the predicates asked for that the rules derive,
	// ordered by predicate, subject and object. A tuple an assertion already
	// states is asserted, not derived, and is not here.
	Derived []graphConclusion `json:"derived"`
	// Lemmas is every derived tuple of any other predicate a proof in Derived
	// cites, in the same order, so every support that is not an assertion can
	// be followed to a proof.
	Lemmas []graphConclusion `json:"lemmas"`
	// Lost is each tuple of the predicates asked for, asserted or derived, that
	// holds with everything and not without the assertions Without names.
	// Empty when Without is absent.
	Lost []graphAtom `json:"lost"`
	// Gained is each that holds only without them — a negation those
	// assertions had been refuting. Empty when Without is absent.
	Gained []graphAtom `json:"gained"`
	// Truncated says a bound stopped the evaluation before its fixpoint. Every
	// tuple listed still holds; some that hold are missing.
	Truncated bool `json:"truncated"`
	// Cut names the bound that stopped it: `tuples`, at Bound tuples derived,
	// or `steps`, at Steps rows examined. Absent exactly when Truncated is
	// false.
	Cut string `json:"cut,omitempty"`
	// Bound is the most tuples one evaluation derives.
	Bound int `json:"bound"`
	// Steps is the most rows one evaluation's joins examine.
	Steps int `json:"steps"`
	// Filed is what filing Derived did, member by member as an assertion batch
	// reports it. Absent unless file was asked.
	Filed *graphAssertOut `json:"filed,omitempty"`
}

// derive concludes what the organization's rules derive from the graph, with a
// proof for every conclusion: the rule, and each support as the assertion in
// force it rests on or as the derived atom it is.
//
// The rules are assertions — (`rule:<name>`, `rule`, `<text>`), Datalog over
// the organization's relations, with recursion, stratified negation and the
// builtins = and != — and the graph is read the way resolve reads it, so a
// retraction, an until and a declared cardinality all apply. Nothing derived is
// stored unless an admin asks to file it. Without names assertions to derive
// without, and the answer then says what would be lost, and what gained, if
// they had never been filed.
//
// Time: as_of and as_known place the graph and the rules it is read with, as
// they place a resolve; either absent is now. Filing takes neither: a filed
// conclusion holds from the instant it was derived.
//
// Example: {"predicates": ["ancestor"], "as_of": "2026-09-01T00:00:00Z"}
func (o ops) derive(ctx context.Context, in *graphDeriveIn) (*graphDeriveOut, error) {
	sc, st, err := tenantOf(ctx, o.s)
	if err != nil {
		return nil, err
	}
	if in.File {
		switch {
		case !admin(ctx):
			return nil, zip.ErrForbidden("filing what the rules derive requires an admin of the organization it acts in: every member reads what is filed")
		case in.AsOf != "" || in.AsKnown != "" || len(in.Without) > 0:
			return nil, zip.ErrBadRequest("a derivation is filed from the graph as it stands now: file takes no as_of, as_known or without")
		case o.s.State.audit == nil:
			return nil, zip.Errorf(http.StatusServiceUnavailable,
				"no durable audit store is configured on this deployment; a filed derivation is recorded before it is filed")
		}
	}
	goals := sorted(in.Predicates)
	switch {
	case len(goals) == 0:
		return nil, zip.ErrBadRequest("predicates is required: name what to derive")
	case len(goals) > seedMax:
		return nil, zip.ErrBadRequest(fmt.Sprintf("a derivation asks for at most %d predicates, not %d", seedMax, len(goals)))
	case len(in.Without) > seedMax:
		return nil, zip.ErrBadRequest(fmt.Sprintf("a derivation is run without at most %d assertions, not %d", seedMax, len(in.Without)))
	}
	w, err := moment(in.AsOf, in.AsKnown)
	if err != nil {
		return nil, err
	}
	rows, cut, err := st.laws(ctx, w.known)
	if err != nil {
		return nil, err
	}
	if cut {
		return nil, zip.ErrBadRequest(fmt.Sprintf("this organization holds more than %d rule assertions, the most one derivation reads", walkBound))
	}
	laws := statute(rows, w.known)
	prog, err := compile(laws)
	if err != nil {
		return nil, zip.ErrBadRequest("the rules in force at this point do not compile: " + err.Error())
	}
	for _, q := range goals {
		switch {
		case !prog.Concludes(q):
			return nil, zip.ErrBadRequest(fmt.Sprintf("no rule in force concludes %s", q))
		case in.File && prog.Arity(q) == 1:
			return nil, zip.ErrBadRequest(fmt.Sprintf("%s takes one argument, so its conclusions state no value to file: "+
				"conclude it with two, as %s(X, \"yes\")", q, q))
		}
	}
	needs := prog.Needs(goals)

	drop := map[string]bool{}
	for _, id := range in.Without {
		drop[id] = true
	}
	var lawsWithout []law
	var progWithout *datalog.Program
	if len(drop) > 0 {
		lawsWithout = statute(slices.DeleteFunc(slices.Clone(rows), func(f Fact) bool { return drop[f.ID] }), w.known)
		if progWithout, err = compile(lawsWithout); err != nil {
			return nil, zip.ErrBadRequest("without those assertions the rules in force do not compile: " + err.Error())
		}
		needs = sorted(slices.Concat(needs, progWithout.Needs(goals)))
	}

	facts, ok, err := st.grounds(ctx, needs, w)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, zip.ErrBadRequest(fmt.Sprintf("the relations these rules read hold more than %d assertions at this instant; "+
			"derive fewer predicates or at an earlier as_of", edgeMax))
	}
	card, err := st.arity(ctx, needs, w.known)
	if err != nil {
		return nil, err
	}

	// Every assertion Without names is one this derivation reads: an ID that
	// names nothing is a what-if that changes nothing, and a typo would say so
	// by answering that nothing falls.
	read := map[string]bool{}
	for _, f := range slices.Concat(rows, facts) {
		read[f.ID] = true
	}
	for _, id := range slices.Sorted(maps.Keys(drop)) {
		if !read[id] {
			return nil, zip.ErrBadRequest(fmt.Sprintf("without names %s, which is no assertion this derivation reads", id))
		}
	}

	d := evaluate(prog, laws, facts, w, card, goals, prog)
	out := &graphDeriveOut{
		AsOf: w.valid.Format(time.RFC3339), AsKnown: w.known.Format(time.RFC3339),
		Lost: []graphAtom{}, Gained: []graphAtom{},
		Truncated: d.model.Cut != "", Cut: d.model.Cut, Bound: deriveMax, Steps: stepMax,
	}
	if len(drop) > 0 {
		e := evaluate(progWithout, lawsWithout, slices.DeleteFunc(slices.Clone(facts), func(f Fact) bool { return drop[f.ID] }), w, card, goals, prog)
		if d.model.Cut != "" || e.model.Cut != "" {
			return nil, zip.ErrBadRequest("a bound cut an evaluation short, so what falls without those assertions cannot be told: " +
				"a difference between two partial answers is not a difference between the answers")
		}
		was, now := d.holds(goals), e.holds(goals)
		for g := range was {
			if !now[g] {
				out.Lost = append(out.Lost, graphAtom{Predicate: g.Pred, Subject: g.Args[0], Object: g.Args[1]})
			}
		}
		for g := range now {
			if !was[g] {
				out.Gained = append(out.Gained, graphAtom{Predicate: g.Pred, Subject: g.Args[0], Object: g.Args[1]})
			}
		}
	}
	atoms := func(a, b graphAtom) int {
		return cmp.Or(cmp.Compare(a.Predicate, b.Predicate), cmp.Compare(a.Subject, b.Subject), cmp.Compare(a.Object, b.Object))
	}
	slices.SortFunc(out.Lost, atoms)
	slices.SortFunc(out.Gained, atoms)
	if in.File && out.Truncated {
		return nil, zip.ErrBadRequest("a derivation a bound cut short is not filed: what it misses would read as not following. " +
			"Derive fewer predicates")
	}
	out.Derived, out.Lemmas = d.answer(goals)
	if in.File {
		if why := d.clash(goals, card); why != "" {
			return nil, zip.ErrBadRequest(why)
		}
		if i := slices.IndexFunc(out.Derived, func(c graphConclusion) bool { return schematic(c.Subject) }); i >= 0 {
			c := out.Derived[i]
			return nil, zip.ErrBadRequest(fmt.Sprintf("%s %s %s is about a declaration, which rests on data any member files: "+
				"a derivation files none, and an admin files one as an assertion of its own", c.Subject, c.Predicate, c.Object))
		}
		if out.Filed, err = o.enter(ctx, sc, st, d, out.Derived, w.valid, goals); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// enter files conclusions as assertions derived at an instant: every one judged
// as a batch member is, and the write witnessed on the audit trail as
// graph.derive — the predicates, how many were filed, and the digest of their
// IDs as the record's resource (file).
func (o ops) enter(ctx context.Context, sc scope, st *store, d *derivation, derived []graphConclusion, at time.Time, goals []string) (*graphAssertOut, error) {
	since := at.Truncate(time.Second).Format(time.RFC3339)
	batch := make([]graphFact, len(derived))
	for i, c := range derived {
		batch[i] = graphFact{
			Entity: c.Subject, Relation: c.Predicate, Value: c.Object,
			Names: d.names(datalog.Ground{Pred: c.Predicate, Args: datalog.Tuple{c.Subject, c.Object}}, 1),
			At:    since, Source: c.Rule, Evidence: c.RuleID, Confidence: c.Decay,
		}
	}
	out, _, err := o.file(ctx, sc, st, batch, time.Now().UTC(), "graph.derive", func(fresh []Fact) ([]string, map[string]any) {
		ids := make([]string, len(fresh))
		for i, f := range fresh {
			ids[i] = f.ID
		}
		return ids, map[string]any{"graph": sc.ns.String(), "predicates": goals, "filed": len(ids)}
	})
	return out, err
}
