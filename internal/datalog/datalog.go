package datalog

import (
	"fmt"
	"strings"
)

// Tuple is one row of a relation. A unary predicate's rows are (x, "").
type Tuple [2]string

// Ground is one ground atom: a predicate and its arguments.
type Ground struct {
	Pred string
	Args Tuple
}

// term is a variable or a constant.
type term struct {
	name  string // the variable's name, or the constant's value
	isVar bool
}

// atom is a predicate applied to terms. A unary atom's second term is the
// constant "".
type atom struct {
	pred  string
	args  [2]term
	arity int
}

// test is a builtin: l = r when eq, else l != r.
type test struct {
	l, r term
	eq   bool
}

// Rule is one parsed, safe rule. Parse is its only constructor.
type Rule struct {
	text  string
	head  atom
	pos   []atom
	neg   []atom
	tests []test
}

// String is the rule as it was written.
func (r Rule) String() string { return r.text }

// Origin is where the head's argument k comes from in a Proof of this rule: the
// first positive atom, in written order, naming the same variable, and the
// column it names it in. ok is false for a constant, and for k past the head's
// arity.
func (r Rule) Origin(k int) (at, col int, ok bool) {
	if k < 0 || k >= r.head.arity || !r.head.args[k].isVar {
		return 0, 0, false
	}
	for i, a := range r.pos {
		for c, t := range a.args[:a.arity] {
			if t == r.head.args[k] {
				return i, c, true
			}
		}
	}
	return 0, 0, false
}

// anonymous is the prefix of the name `_` is given at each occurrence. It holds
// a character no identifier can, so no written variable can collide with it.
const anonymous = "_#"

// Parse reads one rule and refuses it unless it is safe.
func Parse(text string) (Rule, error) {
	text = strings.TrimSpace(text)
	toks, err := lex(text)
	if err != nil {
		return Rule{}, err
	}
	p := &parser{toks: toks}
	r := Rule{text: text}
	if r.head, err = p.atom(); err != nil {
		return Rule{}, err
	}
	if t := p.next(); t.kind != tIf {
		return Rule{}, fmt.Errorf("at %d: expected `:-` after the head, found %s", t.at, t)
	}
	for {
		if err := p.literal(&r); err != nil {
			return Rule{}, err
		}
		if p.peek(0).kind != tComma {
			break
		}
		p.next()
	}
	if p.peek(0).kind == tDot {
		p.next()
	}
	if t := p.peek(0); t.kind != tEOF {
		return Rule{}, fmt.Errorf("at %d: %s after the end of the rule", t.at, t)
	}
	if err := r.safe(); err != nil {
		return Rule{}, err
	}
	return r, nil
}

// safe is the range restriction: every variable of the head, of a negated atom
// and of a builtin is bound by a positive atom of the body.
func (r Rule) safe() error {
	if len(r.pos) == 0 {
		return fmt.Errorf("a rule concludes from at least one positive atom; a fact is asserted, not derived")
	}
	bound := map[string]bool{}
	for _, a := range r.pos {
		for _, t := range a.args[:a.arity] {
			if t.isVar {
				bound[t.name] = true
			}
		}
	}
	check := func(where string, t term) error {
		switch {
		case !t.isVar || bound[t.name]:
			return nil
		case strings.HasPrefix(t.name, anonymous):
			return fmt.Errorf("_ in %s binds nothing: name the variable and bind it in a positive atom", where)
		}
		return fmt.Errorf("%s in %s is bound by no positive atom", t.name, where)
	}
	for _, t := range r.head.args[:r.head.arity] {
		if err := check("the head", t); err != nil {
			return err
		}
	}
	for _, a := range r.neg {
		for _, t := range a.args[:a.arity] {
			if err := check("not "+a.pred, t); err != nil {
				return err
			}
		}
	}
	for _, c := range r.tests {
		for _, t := range []term{c.l, c.r} {
			if err := check("a comparison", t); err != nil {
				return err
			}
		}
	}
	return nil
}

// ── lexing ───────────────────────────────────────────────────────────────────

const (
	tEOF = iota
	tIdent
	tString
	tIf    // :-
	tNe    // !=
	tEq    // =
	tOpen  // (
	tClose // )
	tComma
	tDot
)

type token struct {
	kind int
	text string
	at   int
}

func (t token) String() string {
	switch t.kind {
	case tEOF:
		return "the end of the rule"
	case tString:
		return fmt.Sprintf("%q", t.text)
	}
	return "`" + t.text + "`"
}

func lex(s string) ([]token, error) {
	var out []token
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++
		case c == '_' || letter(c):
			j := i + 1
			for j < len(s) && (s[j] == '_' || letter(s[j]) || digit(s[j])) {
				j++
			}
			out = append(out, token{kind: tIdent, text: s[i:j], at: i})
			i = j
		case c == '"':
			var b strings.Builder
			j := i + 1
			for ; j < len(s) && s[j] != '"'; j++ {
				if s[j] == '\\' {
					if j++; j == len(s) || (s[j] != '"' && s[j] != '\\') {
						return nil, fmt.Errorf("at %d: a constant escapes only \\\" and \\\\", j-1)
					}
				}
				b.WriteByte(s[j])
			}
			if j == len(s) {
				return nil, fmt.Errorf("at %d: the constant is never closed", i)
			}
			out = append(out, token{kind: tString, text: b.String(), at: i})
			i = j + 1
		case strings.HasPrefix(s[i:], ":-"):
			out = append(out, token{kind: tIf, text: ":-", at: i})
			i += 2
		case strings.HasPrefix(s[i:], "!="):
			out = append(out, token{kind: tNe, text: "!=", at: i})
			i += 2
		default:
			k := strings.IndexByte("=(),.", c)
			if k < 0 {
				return nil, fmt.Errorf("at %d: %q is not part of a rule", i, c)
			}
			out = append(out, token{kind: []int{tEq, tOpen, tClose, tComma, tDot}[k], text: string(c), at: i})
			i++
		}
	}
	return append(out, token{kind: tEOF, at: len(s)}), nil
}

func letter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }
func digit(c byte) bool  { return '0' <= c && c <= '9' }

// ── parsing ──────────────────────────────────────────────────────────────────

type parser struct {
	toks []token
	i    int
	anon int
}

func (p *parser) peek(k int) token { return p.toks[min(p.i+k, len(p.toks)-1)] }

func (p *parser) next() token {
	t := p.peek(0)
	if p.i < len(p.toks)-1 {
		p.i++
	}
	return t
}

// literal reads one conjunct of the body into r.
func (p *parser) literal(r *Rule) error {
	t := p.peek(0)
	switch {
	case t.kind == tIdent && t.text == "not" && p.peek(1).kind == tIdent:
		p.next()
		a, err := p.atom()
		if err != nil {
			return err
		}
		r.neg = append(r.neg, a)
	case t.kind == tIdent && p.peek(1).kind == tOpen:
		a, err := p.atom()
		if err != nil {
			return err
		}
		r.pos = append(r.pos, a)
	default:
		l, err := p.term()
		if err != nil {
			return err
		}
		op := p.next()
		if op.kind != tEq && op.kind != tNe {
			return fmt.Errorf("at %d: expected = or != after a term, found %s", op.at, op)
		}
		rt, err := p.term()
		if err != nil {
			return err
		}
		r.tests = append(r.tests, test{l: l, r: rt, eq: op.kind == tEq})
	}
	return nil
}

func (p *parser) atom() (atom, error) {
	t := p.next()
	if t.kind != tIdent || !('a' <= t.text[0] && t.text[0] <= 'z') || t.text == "not" {
		return atom{}, fmt.Errorf("at %d: expected a predicate — a name starting with a lowercase letter, and not `not` — found %s", t.at, t)
	}
	a := atom{pred: t.text, arity: 1}
	if o := p.next(); o.kind != tOpen {
		return atom{}, fmt.Errorf("at %d: expected ( after %s, found %s", o.at, t.text, o)
	}
	var err error
	if a.args[0], err = p.term(); err != nil {
		return atom{}, err
	}
	if p.peek(0).kind == tComma {
		p.next()
		if a.args[1], err = p.term(); err != nil {
			return atom{}, err
		}
		a.arity = 2
	}
	if c := p.next(); c.kind != tClose {
		return atom{}, fmt.Errorf("at %d: %s takes one or two arguments; expected ), found %s", c.at, t.text, c)
	}
	return a, nil
}

func (p *parser) term() (term, error) {
	t := p.next()
	switch {
	case t.kind == tString && t.text == "":
		return term{}, fmt.Errorf("at %d: an empty constant names nothing", t.at)
	case t.kind == tString:
		return term{name: t.text}, nil
	case t.kind == tIdent && t.text == "_":
		p.anon++
		return term{name: fmt.Sprintf("%s%d", anonymous, p.anon), isVar: true}, nil
	case t.kind == tIdent && (t.text[0] == '_' || 'A' <= t.text[0] && t.text[0] <= 'Z'):
		return term{name: t.text, isVar: true}, nil
	case t.kind == tIdent:
		return term{}, fmt.Errorf("at %d: %s is neither a variable nor a constant: a variable starts with an uppercase letter or _, and a constant is quoted", t.at, t.text)
	}
	return term{}, fmt.Errorf("at %d: expected a variable or a quoted constant, found %s", t.at, t)
}
