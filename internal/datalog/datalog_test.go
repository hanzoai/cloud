package datalog

import (
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

// roomy is a bound no test below reaches unless it means to.
var roomy = Bound{Tuples: 1 << 20, Steps: 1 << 26}

func compile(t *testing.T, texts ...string) *Program {
	t.Helper()
	var rules []Rule
	for _, s := range texts {
		r, err := Parse(s)
		if err != nil {
			t.Fatalf("parse %q: %v", s, err)
		}
		rules = append(rules, r)
	}
	p, err := Compile(rules)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return p
}

func derived(m *Model, pred string) []Tuple {
	var out []Tuple
	for _, t := range m.Tuples(pred) {
		if _, ok := m.Proof(Ground{pred, t}); ok {
			out = append(out, t)
		}
	}
	return out
}

// TestAFiveChainHasTenAncestors is recursion: every ordered pair along the chain,
// and nothing else.
func TestAFiveChainHasTenAncestors(t *testing.T) {
	p := compile(t,
		`ancestor(X, Y) :- parent(X, Y).`,
		`ancestor(X, Z) :- parent(X, Y), ancestor(Y, Z).`)
	chain := []string{"a", "b", "c", "d", "e"}
	var parent []Tuple
	for i := range 4 {
		parent = append(parent, Tuple{chain[i], chain[i+1]})
	}
	m := p.Eval(map[string][]Tuple{"parent": parent}, []string{"ancestor"}, roomy)
	if m.Cut != "" {
		t.Fatalf("cut by %s", m.Cut)
	}
	got := derived(m, "ancestor")
	if len(got) != 10 {
		t.Fatalf("ancestor holds %d tuples, want 10: %v", len(got), got)
	}
	for i := range chain {
		for j := i + 1; j < len(chain); j++ {
			if !m.Holds(Ground{"ancestor", Tuple{chain[i], chain[j]}}) {
				t.Errorf("ancestor(%s, %s) is missing", chain[i], chain[j])
			}
		}
	}
	// The proof of the longest is the chain itself, read off the supports.
	var walked []string
	for g := (Ground{"ancestor", Tuple{"a", "e"}}); ; {
		pr, ok := m.Proof(g)
		if !ok {
			t.Fatalf("%v has no proof", g)
		}
		walked = append(walked, pr.Supports[0].Args[0])
		if len(pr.Supports) == 1 {
			walked = append(walked, pr.Supports[0].Args[1])
			break
		}
		g = pr.Supports[1]
	}
	if !slices.Equal(walked, chain) {
		t.Errorf("the proof of ancestor(a, e) walks %v, want %v", walked, chain)
	}
}

// TestNegationYieldsExactlyTheGoodNodes is negation as failure over a complete
// lower stratum — the thing an engine that drops `not` inverts.
func TestNegationYieldsExactlyTheGoodNodes(t *testing.T) {
	p := compile(t, `safe(X) :- node(X), not bad(X)`)
	base := map[string][]Tuple{
		// A unary base relation is its first argument: whatever node says of X.
		"node": {{"n1", "yes"}, {"n2", "yes"}, {"n3", "yes"}, {"n4", "yes"}},
		"bad":  {{"n2", "true"}, {"n4", "true"}, {"n9", "true"}},
	}
	m := p.Eval(base, []string{"safe"}, roomy)
	got := derived(m, "safe")
	if want := []Tuple{{"n1", ""}, {"n3", ""}}; !slices.Equal(got, want) {
		t.Fatalf("safe = %v, want %v", got, want)
	}
}

// TestRecursionThroughNegationIsRefused: no stratum can hold a predicate that
// negates what it is still deriving, directly or through another.
func TestRecursionThroughNegationIsRefused(t *testing.T) {
	for _, set := range [][]string{
		{`p(X) :- q(X), not p(X)`},
		{`p(X) :- q(X), not r(X)`, `r(X) :- q(X), p(X)`},
	} {
		var rules []Rule
		for _, s := range set {
			r, err := Parse(s)
			if err != nil {
				t.Fatalf("parse %q: %v", s, err)
			}
			rules = append(rules, r)
		}
		if _, err := Compile(rules); err == nil || !strings.Contains(err.Error(), "no stratum can hold it") {
			t.Errorf("%v compiled, want refused as unstratifiable; err = %v", set, err)
		}
	}
	// Recursion without negation, and negation of a lower stratum, both stand.
	compile(t, `p(X, Y) :- e(X, Y)`, `p(X, Z) :- p(X, Y), p(Y, Z)`, `q(X) :- n(X), not p(X, X)`)
}

func TestParseRefusesWhatIsNotASafeRule(t *testing.T) {
	for text, says := range map[string]string{
		`p(X) :- q(Y)`:                "X in the head is bound by no positive atom",
		`p(X) :- q(X), not r(X, Y)`:   "Y in not r is bound by no positive atom",
		`p(X) :- q(X), X != Y`:        "Y in a comparison is bound by no positive atom",
		`p(X) :- q(X), not r(X, _)`:   "_ in not r binds nothing",
		`p(_) :- q(X)`:                "_ in the head binds nothing",
		`p(X) :- not q(X)`:            "at least one positive atom",
		`p("a") :- X = "a"`:           "at least one positive atom",
		`p(X) :- q(X, "")`:            "an empty constant names nothing",
		`p(X) :- q(X, a)`:             "a is neither a variable nor a constant",
		`P(X) :- q(X)`:                "expected a predicate",
		`p(X) :- not(X)`:              "expected a predicate",
		`p(X) :- q(X, Y, Z)`:          "takes one or two arguments",
		`p(X) :- q(X) r(X)`:           "after the end of the rule",
		`p(X) q(X)`:                   "expected `:-`",
		`p(X) :- q(X), X < "b"`:       "is not part of a rule",
		`p(X) :- q(X, "unclosed)`:     "never closed",
		`p(X) :- q(X, "bad \n esc")`:  "escapes only",
		`p(X) :- q(X, "a \"b\" \\")`:  "",
		`p(X) :- q(X, "a\x")`:         "escapes only",
		`p(X) :- q(X), X ! "a"`:       "is not part of a rule",
		`p(X) :- q(X), X "a"`:         "expected = or !=",
		`p(X) :- q(X).`:               "",
		`p(X, "k") :- q(X, _), X = X`: "",
	} {
		_, err := Parse(text)
		switch {
		case says == "" && err != nil:
			t.Errorf("Parse(%q) = %v, want it to stand", text, err)
		case says != "" && (err == nil || !strings.Contains(err.Error(), says)):
			t.Errorf("Parse(%q) = %v, want an error saying %q", text, err, says)
		}
	}
}

// TestAHeadArgumentHasOneOrigin: each variable of a head is read from the
// first positive atom that names it, and a constant is read from none.
func TestAHeadArgumentHasOneOrigin(t *testing.T) {
	r, err := Parse(`kin(Y, X) :- parent(X, Z), same(Z, Y), parent(X, Y)`)
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range [][3]int{{1, 1, 1}, {0, 0, 1}} {
		if at, col, ok := r.Origin(k); !ok || [3]int{at, col, 1} != want {
			t.Errorf("Origin(%d) = %d, %d, %v; want atom %d column %d", k, at, col, ok, want[0], want[1])
		}
	}
	c, _ := Parse(`safe(X, "yes") :- node(X)`)
	u, _ := Parse(`seen(X) :- node(X)`)
	for _, tc := range []struct {
		r Rule
		k int
	}{{c, 1}, {u, 1}, {u, 2}, {u, -1}} {
		if _, _, ok := tc.r.Origin(tc.k); ok {
			t.Errorf("%s: Origin(%d) names an atom, want none", tc.r, tc.k)
		}
	}
}

func TestAPredicateHasOneArity(t *testing.T) {
	a, _ := Parse(`p(X) :- q(X, Y)`)
	b, _ := Parse(`r(X) :- q(X)`)
	if _, err := Compile([]Rule{a, b}); err == nil || !strings.Contains(err.Error(), "q takes two arguments") {
		t.Fatalf("q used at two arities compiled: %v", err)
	}
}

// TestTheCeilingIsReportedAndWhatItKeptHolds: a bound stops the evaluation, says
// which bound, and everything it had derived by then is true.
func TestTheCeilingIsReportedAndWhatItKeptHolds(t *testing.T) {
	p := compile(t, `reach(X, Y) :- edge(X, Y)`, `reach(X, Z) :- edge(X, Y), reach(Y, Z)`)
	var edge []Tuple
	for i := range 40 {
		edge = append(edge, Tuple{fmt.Sprint(i), fmt.Sprint(i + 1)})
	}
	whole := p.Eval(map[string][]Tuple{"edge": edge}, []string{"reach"}, roomy)
	if whole.Cut != "" || len(derived(whole, "reach")) != 40*41/2 {
		t.Fatalf("the unbounded run derived %d, cut %q", len(derived(whole, "reach")), whole.Cut)
	}
	for _, b := range []Bound{{Tuples: 100, Steps: roomy.Steps}, {Tuples: roomy.Tuples, Steps: 300}} {
		m := p.Eval(map[string][]Tuple{"edge": edge}, []string{"reach"}, b)
		want := "tuples"
		if b.Tuples == roomy.Tuples {
			want = "steps"
		}
		if m.Cut != want {
			t.Fatalf("bound %+v: cut %q, want %q", b, m.Cut, want)
		}
		got := derived(m, "reach")
		if b.Tuples == 100 && len(got) != 100 {
			t.Errorf("a ceiling of 100 tuples kept %d", len(got))
		}
		for _, r := range got {
			if !whole.Holds(Ground{"reach", r}) {
				t.Errorf("the cut model holds reach%v, which is false", r)
			}
		}
	}
	// Exactly at the ceiling is not over it: a fixpoint of N tuples under a
	// ceiling of N is complete.
	if m := p.Eval(map[string][]Tuple{"edge": edge}, []string{"reach"}, Bound{Tuples: 40 * 41 / 2, Steps: roomy.Steps}); m.Cut != "" {
		t.Errorf("a fixpoint exactly at the ceiling reported cut %q", m.Cut)
	}
}

// TestOnlyWhatTheGoalsNeedIsEvaluated: a stratum the goals do not reach is not
// run, so its cost is not paid — and it cannot trip the ceiling.
func TestOnlyWhatTheGoalsNeedIsEvaluated(t *testing.T) {
	p := compile(t, `a(X) :- n(X)`, `big(X, Y) :- n(X), n(Y)`)
	if got := p.Needs([]string{"a"}); !slices.Equal(got, []string{"a", "n"}) {
		t.Fatalf("Needs(a) = %v", got)
	}
	var n []Tuple
	for i := range 100 {
		n = append(n, Tuple{fmt.Sprint(i), "x"})
	}
	m := p.Eval(map[string][]Tuple{"n": n}, []string{"a"}, Bound{Tuples: 200, Steps: roomy.Steps})
	if m.Cut != "" || len(derived(m, "a")) != 100 || len(m.Tuples("big")) != 0 {
		t.Fatalf("cut %q, a %d, big %d", m.Cut, len(derived(m, "a")), len(m.Tuples("big")))
	}
}

// ── the differential test ───────────────────────────────────────────────────

// TestSemiNaiveAgreesWithANaiveOracle evaluates random stratified programs over
// random graphs twice — by this engine, and by the most literal evaluator that
// could be written: every rule over every whole relation until nothing changes,
// strata found by iterating levels rather than by components — and demands the
// same model. It then checks every proof: the rule, instantiated, has exactly
// that head and those supports, its negations and comparisons hold, and no proof
// reaches back to its own conclusion.
//
// The last lines hold the generator to its job: a differential test over
// programs that derive nothing, never recurse or never negate agrees vacuously.
func TestSemiNaiveAgreesWithANaiveOracle(t *testing.T) {
	var proved, recursive, negating int
	for seed := range 24 {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(uint64(seed), 0x5eed))
			rules, texts := program(rng)
			p, err := Compile(rules)
			if err != nil {
				t.Fatalf("the generator kept an uncompilable program: %v\n%s", err, strings.Join(texts, "\n"))
			}
			base := graph(rng)
			goals := slices.Sorted(maps.Keys(p.deps))
			m := p.Eval(base, goals, roomy)
			if m.Cut != "" {
				t.Fatalf("cut by %s", m.Cut)
			}
			want := naive(rules, base, p.arity)
			for _, q := range p.Needs(goals) {
				got := slices.Clone(m.Tuples(q))
				sortTuples(got)
				if w := want[q]; !slices.Equal(got, w) {
					t.Fatalf("%s: engine %v\noracle %v\nprogram:\n%s", q, got, w, strings.Join(texts, "\n"))
				}
			}
			verify(t, p, m, goals)
			proved += len(m.proofs)
			for _, s := range p.strata {
				for _, ri := range s.rules {
					if slices.ContainsFunc(p.rules[ri].pos, func(a atom) bool { return s.preds[a.pred] }) {
						recursive++
					}
				}
			}
			for _, r := range rules {
				negating += min(len(r.neg), 1)
			}

			// The order base arrives in changes nothing, proofs included.
			shuffled := map[string][]Tuple{}
			for q, ts := range base {
				ts = slices.Clone(ts)
				rng.Shuffle(len(ts), func(i, j int) { ts[i], ts[j] = ts[j], ts[i] })
				shuffled[q] = ts
			}
			again := p.Eval(shuffled, goals, roomy)
			for g, pr := range m.proofs {
				if other, ok := again.Proof(g); !ok || other.Rule != pr.Rule || !slices.Equal(other.Supports, pr.Supports) {
					t.Fatalf("%v is proved differently when base arrives in another order", g)
				}
			}
		})
	}
	t.Logf("%d tuples proved, %d recursive rules, %d negating rules", proved, recursive, negating)
	if proved < 500 || recursive < 24 || negating < 24 {
		t.Errorf("the generator is too tame to test anything: %d proved, %d recursive, %d negating", proved, recursive, negating)
	}
}

// program is a random stratified program over base predicates e0..e2 (binary)
// and u0, u1 (unary) and derived predicates d0..d2 (binary) and v0, v1 (unary).
// Bodies chain — an atom usually starts from a variable an earlier one bound —
// and a third of the binary rules are closure steps, so programs join, recurse
// and derive rather than filter everything away. A candidate rule that would
// make the set unstratifiable is dropped, so what is kept exercises recursion,
// negation of lower strata, constants, `_` and both builtins.
func program(rng *rand.Rand) ([]Rule, []string) {
	preds := map[string]int{"e0": 2, "e1": 2, "e2": 2, "u0": 1, "u1": 1, "d0": 2, "d1": 2, "d2": 2, "v0": 1, "v1": 1}
	all := slices.Sorted(maps.Keys(preds))
	binary := []string{"e0", "e1", "e2", "d0", "d1", "d2"}
	heads := []string{"d0", "d1", "d2", "v0", "v1"}
	constant := func() string { return fmt.Sprintf("%q", fmt.Sprint("c", rng.IntN(8))) }
	var rules []Rule
	var texts []string
	for len(rules) < 7 {
		h := heads[rng.IntN(len(heads))]
		var bound, body []string
		fresh := func() string {
			v := string(rune('A' + len(bound)))
			bound = append(bound, v)
			return v
		}
		pick := func() string { return bound[rng.IntN(len(bound))] }
		// two is two bound variables, distinct whenever the body bound two.
		two := func() (string, string) {
			i := rng.IntN(len(bound))
			return bound[i], bound[(i+1+rng.IntN(max(len(bound)-1, 1)))%len(bound)]
		}
		if preds[h] == 2 && rng.IntN(3) == 0 {
			step := binary[rng.IntN(len(binary))]
			x, y, z := fresh(), fresh(), fresh()
			body = append(body, fmt.Sprintf("%s(%s, %s)", h, x, y), fmt.Sprintf("%s(%s, %s)", step, y, z))
			if rng.IntN(2) == 0 {
				body[0], body[1] = fmt.Sprintf("%s(%s, %s)", step, x, y), fmt.Sprintf("%s(%s, %s)", h, y, z)
			}
		} else {
			for range 1 + rng.IntN(3) {
				q := all[rng.IntN(len(all))]
				var args []string
				for i := range preds[q] {
					switch k := rng.IntN(12); {
					case k == 0:
						args = append(args, constant())
					case k == 1:
						args = append(args, "_")
					case len(bound) > 0 && (i == 0 && k < 9 || k < 4):
						args = append(args, pick())
					default:
						args = append(args, fresh())
					}
				}
				body = append(body, fmt.Sprintf("%s(%s)", q, strings.Join(args, ", ")))
			}
		}
		if len(bound) == 0 {
			continue
		}
		if rng.IntN(3) == 0 {
			q := all[rng.IntN(len(all))]
			a, b := two()
			args := []string{a}
			if preds[q] == 2 {
				args = append(args, b)
			}
			body = append(body, fmt.Sprintf("not %s(%s)", q, strings.Join(args, ", ")))
		}
		switch rng.IntN(5) {
		case 0:
			a, b := two()
			body = append(body, a+" != "+b)
		case 1:
			body = append(body, pick()+" = "+constant())
		}
		a, b := two()
		args := []string{a}
		if preds[h] == 2 {
			args = append(args, b)
		}
		text := fmt.Sprintf("%s(%s) :- %s.", h, strings.Join(args, ", "), strings.Join(body, ", "))
		r, err := Parse(text)
		if err != nil {
			panic(fmt.Sprintf("the generator wrote an unsafe rule %q: %v", text, err))
		}
		if _, err := Compile(append(slices.Clone(rules), r)); err != nil {
			continue
		}
		rules, texts = append(rules, r), append(texts, text)
	}
	return rules, texts
}

// graph is random base tuples over eight constants, some of them for derived
// predicates, whose base tuples join what the rules derive.
func graph(rng *rand.Rand) map[string][]Tuple {
	c := func() string { return fmt.Sprint("c", rng.IntN(8)) }
	out := map[string][]Tuple{}
	for _, q := range []string{"e0", "e1", "e2", "d1"} {
		for range 8 + rng.IntN(16) {
			out[q] = append(out[q], Tuple{c(), c()})
		}
	}
	for _, q := range []string{"u0", "u1", "v1"} {
		for range 2 + rng.IntN(5) {
			out[q] = append(out[q], Tuple{c(), "anything"})
		}
	}
	return out
}

func sortTuples(ts []Tuple) {
	slices.SortFunc(ts, func(a, b Tuple) int { return strings.Compare(a[0]+"\x00"+a[1], b[0]+"\x00"+b[1]) })
}

// naive is the oracle. Levels are found by relaxation — a head sits above what it
// reads, strictly above what it negates — and each level is evaluated by
// applying every rule to every whole relation until a pass adds nothing.
func naive(rules []Rule, base map[string][]Tuple, arity map[string]int) map[string][]Tuple {
	level := map[string]int{}
	for range len(arity) + 1 {
		for _, r := range rules {
			l := level[r.head.pred]
			for _, a := range r.pos {
				l = max(l, level[a.pred])
			}
			for _, a := range r.neg {
				l = max(l, level[a.pred]+1)
			}
			level[r.head.pred] = l
		}
	}
	rel := map[string]map[Tuple]bool{}
	for q := range arity {
		rel[q] = map[Tuple]bool{}
		for _, t := range base[q] {
			if arity[q] == 1 {
				t[1] = ""
			}
			rel[q][t] = true
		}
	}
	top := slices.Max(slices.Collect(maps.Values(level)))
	for l := 0; l <= top; l++ {
		for changed := true; changed; {
			changed = false
			for _, r := range rules {
				if level[r.head.pred] != l {
					continue
				}
				for _, env := range matches(r.pos, rel, map[string]string{}) {
					if !admits(r, rel, env) {
						continue
					}
					h := ground(r.head, env)
					if !rel[r.head.pred][h] {
						rel[r.head.pred][h] = true
						changed = true
					}
				}
			}
		}
	}
	out := map[string][]Tuple{}
	for q, set := range rel {
		ts := slices.Collect(maps.Keys(set))
		sortTuples(ts)
		out[q] = ts
	}
	return out
}

// matches is every binding of the atoms' variables that some whole relation
// tuple satisfies, by nested loops.
func matches(atoms []atom, rel map[string]map[Tuple]bool, env map[string]string) []map[string]string {
	if len(atoms) == 0 {
		return []map[string]string{maps.Clone(env)}
	}
	var out []map[string]string
	for t := range rel[atoms[0].pred] {
		next := maps.Clone(env)
		if unify(atoms[0], t, next) {
			out = append(out, matches(atoms[1:], rel, next)...)
		}
	}
	return out
}

func unify(a atom, t Tuple, env map[string]string) bool {
	for i, x := range a.args {
		switch v, ok := env[x.name]; {
		case !x.isVar:
			if t[i] != x.name {
				return false
			}
		case ok:
			if v != t[i] {
				return false
			}
		default:
			env[x.name] = t[i]
		}
	}
	return true
}

func value(x term, env map[string]string) string {
	if x.isVar {
		return env[x.name]
	}
	return x.name
}

func ground(a atom, env map[string]string) Tuple {
	return Tuple{value(a.args[0], env), value(a.args[1], env)}
}

func admits(r Rule, rel map[string]map[Tuple]bool, env map[string]string) bool {
	for _, a := range r.neg {
		if rel[a.pred][ground(a, env)] {
			return false
		}
	}
	for _, c := range r.tests {
		if (value(c.l, env) == value(c.r, env)) != c.eq {
			return false
		}
	}
	return true
}

// verify checks every proof in m: its rule, instantiated by unifying the body
// with the supports and the head with the conclusion, holds in the model; and
// the proofs form a DAG.
func verify(t *testing.T, p *Program, m *Model, goals []string) {
	t.Helper()
	rel := map[string]map[Tuple]bool{}
	for q, r := range m.rels {
		rel[q] = r.set
	}
	for g, pr := range m.proofs {
		r := p.rules[pr.Rule]
		env := map[string]string{}
		if r.head.pred != g.Pred || !unify(r.head, g.Args, env) || len(pr.Supports) != len(r.pos) {
			t.Fatalf("%v is not the head of %q", g, r.text)
		}
		for i, a := range r.pos {
			s := pr.Supports[i]
			if s.Pred != a.pred || !unify(a, s.Args, env) || !m.Holds(s) {
				t.Fatalf("%v: support %v does not match %s of %q, or is not in the model", g, s, a.pred, r.text)
			}
		}
		if !admits(r, rel, env) {
			t.Fatalf("%v: %q's negations or comparisons fail under %v", g, r.text, env)
		}
	}
	state := map[Ground]int{} // 1 on the stack, 2 done
	var visit func(g Ground)
	visit = func(g Ground) {
		switch state[g] {
		case 1:
			t.Fatalf("the proof of %v reaches %v again", g, g)
		case 2:
			return
		}
		state[g] = 1
		if pr, ok := m.proofs[g]; ok {
			for _, s := range pr.Supports {
				visit(s)
			}
		}
		state[g] = 2
	}
	for _, q := range goals {
		for _, tu := range m.Tuples(q) {
			visit(Ground{q, tu})
		}
	}
}
