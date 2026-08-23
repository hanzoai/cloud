package fleet

// graphql.go answers a GraphQL request against the WHOLE fleet.
//
// The schema it answers is openapi.GraphQL over this deployment's composed
// document, and the dispatch is openapi.Fields over the SAME document at the same
// moment — so a field a caller can read is a field a caller can send, by
// construction rather than by two lists agreeing.
//
// It parses here rather than borrowing a parser because the two available ones
// cannot be reached: zip's executes against a process's own typed registry, which
// in the light host is nearly empty, and graph-gophers' parser is internal to a
// package whose executor wants Go resolver structs — there is no struct with
// sixteen hundred methods to give it.
//
// EXECUTION IS ONE HOP PER ROOT FIELD, in parallel, each carrying the caller's own
// headers to the app that owns it. That is [ask]'s mechanism with the operation's
// address instead of the app's door: the child authenticates the caller, scopes
// the tenant and answers as itself, exactly as it would over REST. Nothing here
// holds an identity or decides an authorization — a field reaches precisely what
// its REST route reaches, for the caller who asked.

import (
	"fmt"
	"strconv"
	"strings"
)

// Request is one GraphQL request as a client sends it.
type Request struct {
	Query     string         `json:"query"`
	Operation string         `json:"operationName,omitempty"`
	Variables map[string]any `json:"variables,omitempty"`
}

// Response is what one comes back as. Data is absent when execution never began;
// once it has, the answer is 200 and the failures ride in Errors, which is where
// a GraphQL client reads them.
type Response struct {
	Data   map[string]any `json:"data,omitempty"`
	Errors []Failure      `json:"errors,omitempty"`
}

// Failure is one field's reason, named by the path it happened at.
type Failure struct {
	Message string   `json:"message"`
	Path    []string `json:"path,omitempty"`
}

// ── the query language ───────────────────────────────────────────────────────

type selection struct {
	alias  string
	name   string
	args   map[string]any
	sels   []selection
	spread string // a fragment named here rather than selected
}

type operation struct {
	kind string
	name string
	sels []selection
}

type parser struct {
	s    string
	i    int
	err  error
	last string // the keyword word() matched, so query and mutation are told apart
}

// parse reads a document into its operations and fragments.
func parse(q string) ([]operation, map[string][]selection, error) {
	p := &parser{s: q}
	var ops []operation
	frags := map[string][]selection{}

	for {
		p.space()
		if p.done() {
			break
		}
		switch {
		case p.peek() == '{':
			ops = append(ops, operation{kind: "query", sels: p.selections()})
		case p.word("query"), p.word("mutation"):
			kind := p.last
			p.space()
			name := p.name()
			p.vardefs()
			ops = append(ops, operation{kind: kind, name: name, sels: p.selections()})
		case p.word("fragment"):
			p.space()
			name := p.name()
			p.space()
			if p.word("on") {
				p.space()
				p.name()
			}
			frags[name] = p.selections()
		default:
			return nil, nil, fmt.Errorf("unexpected %q at %d", string(p.peek()), p.i)
		}
		if p.err != nil {
			return nil, nil, p.err
		}
	}
	if len(ops) == 0 {
		return nil, nil, fmt.Errorf("no operation to run")
	}
	return ops, frags, nil
}

func (p *parser) done() bool { return p.i >= len(p.s) }
func (p *parser) peek() byte {
	if p.done() {
		return 0
	}
	return p.s[p.i]
}

// space skips whitespace, commas (which GraphQL treats as whitespace) and
// comments.
func (p *parser) space() {
	for !p.done() {
		switch c := p.s[p.i]; {
		case c == ' ', c == '\t', c == '\n', c == '\r', c == ',':
			p.i++
		case c == '#':
			for !p.done() && p.s[p.i] != '\n' {
				p.i++
			}
		default:
			return
		}
	}
}

// wordAt reports a keyword at the cursor, and only when what follows it is not
// more name — `queryFoo` is a field, not the keyword `query`.
func (p *parser) wordAt(w string) bool {
	if strings.HasPrefix(p.s[p.i:], w) {
		j := p.i + len(w)
		if j >= len(p.s) || !isName(p.s[j]) {
			return true
		}
	}
	return false
}

func (p *parser) word(w string) bool {
	p.space()
	if p.wordAt(w) {
		p.i += len(w)
		p.last = w
		return true
	}
	return false
}

func isName(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func (p *parser) name() string {
	p.space()
	start := p.i
	for !p.done() && isName(p.s[p.i]) {
		p.i++
	}
	return p.s[start:p.i]
}

// vardefs skips a variable definition list. The DECLARED types are not consulted:
// what a variable is worth arrives with the request, and the child validates the
// value it actually receives.
func (p *parser) vardefs() {
	p.space()
	if p.peek() != '(' {
		return
	}
	depth := 0
	for !p.done() {
		switch p.s[p.i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				p.i++
				return
			}
		}
		p.i++
	}
}

func (p *parser) selections() []selection {
	p.space()
	if p.peek() != '{' {
		p.err = fmt.Errorf("expected a selection set at %d", p.i)
		return nil
	}
	p.i++
	var out []selection
	for {
		p.space()
		if p.done() {
			p.err = fmt.Errorf("unclosed selection set")
			return out
		}
		if p.peek() == '}' {
			p.i++
			return out
		}
		if strings.HasPrefix(p.s[p.i:], "...") {
			p.i += 3
			p.space()
			if p.word("on") {
				p.space()
				p.name()
				out = append(out, selection{sels: p.selections(), name: "..."})
				continue
			}
			out = append(out, selection{spread: p.name()})
			continue
		}
		f := selection{name: p.name()}
		if f.name == "" {
			p.err = fmt.Errorf("expected a field at %d", p.i)
			return out
		}
		p.space()
		if p.peek() == ':' {
			p.i++
			f.alias, f.name = f.name, p.name()
		}
		f.args = p.args()
		p.space()
		if p.peek() == '{' {
			f.sels = p.selections()
		}
		out = append(out, f)
	}
}

func (p *parser) args() map[string]any {
	p.space()
	if p.peek() != '(' {
		return nil
	}
	p.i++
	out := map[string]any{}
	for {
		p.space()
		if p.done() || p.peek() == ')' {
			p.i++
			return out
		}
		k := p.name()
		p.space()
		if p.peek() == ':' {
			p.i++
		}
		out[k] = p.value()
	}
}

// variable is a placeholder resolved against the request's variables at
// execution, so one parse serves any values.
type variable string

func (p *parser) value() any {
	p.space()
	switch c := p.peek(); {
	case c == '$':
		p.i++
		return variable(p.name())
	case c == '"':
		return p.text()
	case c == '[':
		p.i++
		var out []any
		for {
			p.space()
			if p.done() || p.peek() == ']' {
				p.i++
				return out
			}
			out = append(out, p.value())
		}
	case c == '{':
		p.i++
		out := map[string]any{}
		for {
			p.space()
			if p.done() || p.peek() == '}' {
				p.i++
				return out
			}
			k := p.name()
			p.space()
			if p.peek() == ':' {
				p.i++
			}
			out[k] = p.value()
		}
	case c == '-' || (c >= '0' && c <= '9'):
		start := p.i
		p.i++
		for !p.done() && (isName(p.s[p.i]) || p.s[p.i] == '.' || p.s[p.i] == '-' || p.s[p.i] == '+') {
			p.i++
		}
		lit := p.s[start:p.i]
		if n, err := strconv.ParseInt(lit, 10, 64); err == nil {
			return n
		}
		if f, err := strconv.ParseFloat(lit, 64); err == nil {
			return f
		}
		return lit
	}
	switch n := p.name(); n {
	case "true":
		return true
	case "false":
		return false
	case "null":
		return nil
	default:
		// An enum. On the wire it is the name itself, which is what every one of
		// these operations declared it as.
		return n
	}
}

// text reads a string, including the block form, because a client that pretty-
// prints a long argument sends one.
func (p *parser) text() string {
	if strings.HasPrefix(p.s[p.i:], `"""`) {
		p.i += 3
		j := strings.Index(p.s[p.i:], `"""`)
		if j < 0 {
			p.err = fmt.Errorf("unclosed block string")
			return ""
		}
		out := p.s[p.i : p.i+j]
		p.i += j + 3
		return out
	}
	p.i++
	var b strings.Builder
	for !p.done() {
		c := p.s[p.i]
		switch c {
		case '"':
			p.i++
			return b.String()
		case '\\':
			p.i++
			if p.done() {
				return b.String()
			}
			switch p.s[p.i] {
			case 'n':
				b.WriteByte('\n')
			case 't':
				b.WriteByte('\t')
			case 'r':
				b.WriteByte('\r')
			default:
				b.WriteByte(p.s[p.i])
			}
			p.i++
		default:
			b.WriteByte(c)
			p.i++
		}
	}
	p.err = fmt.Errorf("unclosed string")
	return b.String()
}
