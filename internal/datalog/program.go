package datalog

// program.go is a rule set made evaluable: every predicate at one arity, and the
// predicates ordered into strata by their dependency graph.

import (
	"fmt"
	"maps"
	"slices"
)

// Program is a set of rules that stratifies. Compile is its only constructor.
type Program struct {
	rules []Rule
	arity map[string]int
	// deps is each predicate's body predicates, positive and negated, sorted.
	deps map[string][]string
	// strata are the components that some rule concludes, dependencies first.
	strata []stratum
}

// stratum is one strongly connected component of the dependency graph and the
// rules whose heads are in it.
type stratum struct {
	preds map[string]bool
	rules []int
}

// Compile checks that the rules agree on every predicate's arity and that no
// rule negates a predicate inside its own recursion, and orders them for
// evaluation. Rules keep their positions: a Proof names a rule by its index in
// the slice given here.
func Compile(rules []Rule) (*Program, error) {
	p := &Program{rules: rules, arity: map[string]int{}, deps: map[string][]string{}}
	first := map[string]int{}
	use := func(a atom, i int) error {
		n, ok := p.arity[a.pred]
		if !ok {
			p.arity[a.pred], first[a.pred] = a.arity, i
			return nil
		}
		if n != a.arity {
			return fmt.Errorf("%s takes %s in %q and %s in %q", a.pred,
				arguments(n), rules[first[a.pred]].text, arguments(a.arity), rules[i].text)
		}
		return nil
	}
	for i, r := range rules {
		for _, a := range slices.Concat([]atom{r.head}, r.pos, r.neg) {
			if err := use(a, i); err != nil {
				return nil, err
			}
		}
		for _, a := range slices.Concat(r.pos, r.neg) {
			p.deps[r.head.pred] = append(p.deps[r.head.pred], a.pred)
		}
	}
	for k, v := range p.deps {
		slices.Sort(v)
		p.deps[k] = slices.Compact(v)
	}

	comps := components(slices.Sorted(maps.Keys(p.arity)), p.deps)
	in := map[string]int{}
	for c, preds := range comps {
		for _, q := range preds {
			in[q] = c
		}
	}
	for _, r := range rules {
		for _, a := range r.neg {
			if in[a.pred] == in[r.head.pred] {
				return nil, fmt.Errorf("%q negates %s inside the recursion that defines %s, so no stratum can hold it: "+
					"negation reads a relation that is already complete, and this one is still being derived",
					r.text, a.pred, r.head.pred)
			}
		}
	}
	for c, preds := range comps {
		s := stratum{preds: map[string]bool{}}
		for _, q := range preds {
			s.preds[q] = true
		}
		for i, r := range rules {
			if in[r.head.pred] == c {
				s.rules = append(s.rules, i)
			}
		}
		if len(s.rules) > 0 {
			p.strata = append(p.strata, s)
		}
	}
	return p, nil
}

func arguments(n int) string {
	if n == 1 {
		return "one argument"
	}
	return "two arguments"
}

// components is Tarjan's strongly connected components of the graph `next`
// draws over nodes. A component is emitted only after every component it
// reaches, so with edges from a head to its body the order is dependencies
// first — the order a stratified program is evaluated in.
func components(nodes []string, next map[string][]string) [][]string {
	index, low := map[string]int{}, map[string]int{}
	on := map[string]bool{}
	var stack []string
	var out [][]string
	var visit func(v string)
	visit = func(v string) {
		n := len(index)
		index[v], low[v] = n, n
		stack = append(stack, v)
		on[v] = true
		for _, w := range next[v] {
			if _, seen := index[w]; !seen {
				visit(w)
				low[v] = min(low[v], low[w])
			} else if on[w] {
				low[v] = min(low[v], index[w])
			}
		}
		if low[v] != index[v] {
			return
		}
		var c []string
		for {
			w := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			on[w] = false
			c = append(c, w)
			if w == v {
				break
			}
		}
		slices.Sort(c)
		out = append(out, c)
	}
	for _, v := range nodes {
		if _, seen := index[v]; !seen {
			visit(v)
		}
	}
	return out
}

// Arity is how many arguments the program gives a predicate, and 0 for one it
// does not name.
func (p *Program) Arity(pred string) int { return p.arity[pred] }

// Concludes reports whether some rule of the program has pred as its head.
func (p *Program) Concludes(pred string) bool {
	_, ok := p.deps[pred]
	return ok
}

// Needs is every predicate an evaluation for goals reads: the goals and every
// predicate they depend on, sorted. Base tuples for any other predicate cannot
// change what the goals derive.
func (p *Program) Needs(goals []string) []string {
	return slices.Sorted(maps.Keys(p.cone(goals)))
}

func (p *Program) cone(goals []string) map[string]bool {
	seen := map[string]bool{}
	stack := slices.Clone(goals)
	for len(stack) > 0 {
		q := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[q] {
			continue
		}
		seen[q] = true
		stack = append(stack, p.deps[q]...)
	}
	return seen
}
