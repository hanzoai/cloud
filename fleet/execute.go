package fleet

// execute.go runs a parsed request against the fleet: one hop per root field, in
// parallel, each to the app that owns the operation.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/openapi"
	"github.com/valyala/fasthttp"
)

// The two ceilings on one request, and they bound the WORK rather than only the
// answer.
//
// A root field is a HOP: it starts a lazy child if one is not running and holds a
// connection while that child answers. So the width of a query is fan-out into
// the fleet, and an unbounded one lets a single caller ask this host to dial every
// app it composes, several times over, in parallel. Depth costs no hop — nesting
// only narrows an answer already in hand — but it is still a caller-supplied
// recursion, and expansion copies what a fragment spreads.
//
// Both REFUSE rather than truncate. A door that quietly answered the first
// sixty-four of a hundred fields would report a complete-looking answer that is
// missing a third of what was asked, and the caller has no way to tell. Refusing
// says which ceiling bound and how far over, so the caller can split the request.
const (
	rootMax  = 64
	depthMax = 32
)

// Graph is the door's dispatch table: the fields this deployment publishes, and
// the way to reach the app behind each one.
//
// Built from the composed document, which is also what the schema is rendered
// from — one value, two projections, so the schema cannot advertise a field the
// table cannot send.
type Graph struct {
	fields map[string]openapi.Field
	at     At
}

// NewGraph indexes a composed document for execution.
func NewGraph(d *openapi.Document, at At) *Graph {
	return &Graph{fields: openapi.Fields(d), at: at}
}

// Fields is how many operations this door can dispatch.
func (g *Graph) Fields() int { return len(g.fields) }

// Run answers one request. `from` is the caller's own request, whose headers ride
// to every child so each one authenticates and scopes the caller exactly as it
// would over REST — this door decides no authorization of its own.
func (g *Graph) Run(req Request, from *fasthttp.Request) Response {
	ops, frags, err := parse(req.Query)
	if err != nil {
		return Response{Errors: []Failure{{Message: err.Error()}}}
	}
	op, err := pick(ops, req.Operation)
	if err != nil {
		return Response{Errors: []Failure{{Message: err.Error()}}}
	}

	roots, err := expand(op.sels, frags, map[string]bool{}, 0)
	if err != nil {
		return Response{Errors: []Failure{{Message: err.Error()}}}
	}
	if len(roots) > rootMax {
		return Response{Errors: []Failure{{Message: fmt.Sprintf(
			"%d root fields is %d over the %d a request may ask for; each one is a hop into the fleet",
			len(roots), len(roots)-rootMax, rootMax)}}}
	}

	data := make(map[string]any, len(roots))
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		errs []Failure
	)
	for _, sel := range roots {
		wg.Add(1)
		go func(sel selection) {
			defer wg.Done()
			key := sel.alias
			if key == "" {
				key = sel.name
			}
			value, err := g.resolve(sel, req.Variables, from)
			mu.Lock()
			defer mu.Unlock()
			// A field that failed is null WITH a reason, never absent: a client that
			// asked for five things and got four cannot tell which one is missing.
			data[key] = value
			if err != nil {
				errs = append(errs, Failure{Message: err.Error(), Path: []string{key}})
			}
		}(sel)
	}
	wg.Wait()
	return Response{Data: data, Errors: errs}
}

// pick chooses the operation to run: the one named, or the only one there is.
func pick(ops []operation, name string) (operation, error) {
	if name == "" {
		if len(ops) == 1 {
			return ops[0], nil
		}
		return operation{}, fmt.Errorf("this document holds %d operations; name the one to run", len(ops))
	}
	for _, op := range ops {
		if op.name == name {
			return op, nil
		}
	}
	return operation{}, fmt.Errorf("no operation named %q", name)
}

// expand replaces fragment spreads with what they select. The seen set is what
// stops a fragment that spreads itself — a cycle GraphQL forbids and a parser
// cannot refuse on its own.
func expand(sels []selection, frags map[string][]selection, seen map[string]bool, depth int) ([]selection, error) {
	if depth > depthMax {
		return nil, fmt.Errorf("selections nest deeper than %d", depthMax)
	}
	var out []selection
	for _, s := range sels {
		if s.spread != "" {
			if seen[s.spread] {
				return nil, fmt.Errorf("fragment %q spreads itself", s.spread)
			}
			body, ok := frags[s.spread]
			if !ok {
				return nil, fmt.Errorf("no fragment named %q", s.spread)
			}
			seen[s.spread] = true
			inner, err := expand(body, frags, seen, depth+1)
			delete(seen, s.spread)
			if err != nil {
				return nil, err
			}
			out = append(out, inner...)
			continue
		}
		// An inline fragment contributes its selections here: this door has one
		// type per field, so there is no condition left to test.
		if s.name == "..." {
			inner, err := expand(s.sels, frags, seen, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, inner...)
			continue
		}
		if len(s.sels) > 0 {
			inner, err := expand(s.sels, frags, seen, depth+1)
			if err != nil {
				return nil, err
			}
			s.sels = inner
		}
		out = append(out, s)
	}
	return out, nil
}

// resolve is one root field: one request to one app, and the part of its answer
// the caller selected.
func (g *Graph) resolve(sel selection, vars map[string]any, from *fasthttp.Request) (any, error) {
	f, ok := g.fields[sel.name]
	if !ok {
		return nil, fmt.Errorf("no field named %q", sel.name)
	}
	args, err := settle(sel.args, vars)
	if err != nil {
		return nil, err
	}

	path, err := address(f, args)
	if err != nil {
		return nil, err
	}
	var body []byte
	if raw, sent := args["body"]; sent {
		if body, err = json.Marshal(raw); err != nil {
			return nil, fmt.Errorf("body: %w", err)
		}
	}

	answer := g.send(f, path, body, from)
	if answer.Err != nil {
		return nil, answer.Err
	}
	var out any
	if len(answer.Body) > 0 {
		if err := json.Unmarshal(answer.Body, &out); err != nil {
			// A child that answered something other than JSON still answered. The
			// bytes are the honest result and hiding them would report a working
			// operation as broken.
			return string(answer.Body), nil
		}
	}
	if len(sel.sels) == 0 {
		return out, nil
	}
	return narrow(out, sel.sels), nil
}

// settle resolves variable placeholders against the values the request carried.
func settle(args map[string]any, vars map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(args))
	for k, v := range args {
		s, err := settleOne(v, vars)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", k, err)
		}
		out[k] = s
	}
	return out, nil
}

func settleOne(v any, vars map[string]any) (any, error) {
	switch t := v.(type) {
	case variable:
		val, ok := vars[string(t)]
		if !ok {
			return nil, fmt.Errorf("variable $%s was not given a value", string(t))
		}
		return val, nil
	case []any:
		out := make([]any, 0, len(t))
		for _, e := range t {
			s, err := settleOne(e, vars)
			if err != nil {
				return nil, err
			}
			out = append(out, s)
		}
		return out, nil
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			s, err := settleOne(e, vars)
			if err != nil {
				return nil, err
			}
			out[k] = s
		}
		return out, nil
	}
	return v, nil
}

// address turns the operation's path template plus the caller's arguments into
// the address to send. A path parameter with no argument is refused here rather
// than sent as a literal brace the child would 404 on.
func address(f openapi.Field, args map[string]any) (string, error) {
	path := f.Path
	for _, name := range f.Route {
		v, ok := args[name]
		if !ok {
			return "", fmt.Errorf("%s is part of the address and was not given", name)
		}
		path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(literal(v)))
	}
	q := url.Values{}
	for _, name := range f.Query {
		if v, ok := args[name]; ok && v != nil {
			q.Set(name, literal(v))
		}
	}
	if len(q) > 0 {
		return path + "?" + q.Encode(), nil
	}
	return path, nil
}

// literal renders one argument as the wire spells it. A composite in a query
// string goes as JSON, which is what every one of these operations already
// accepts there.
func literal(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	case bool, int, int32, int64, float32, float64, json.Number:
		return fmt.Sprint(t)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(b)
}

// send is [ask] with the OPERATION's address instead of the app's own door: the
// same resolution, the same pooled transport, the same copied request. The
// caller's headers ride along, so the child answers as itself for whoever asked.
func (g *Graph) send(f openapi.Field, path string, body []byte, from *fasthttp.Request) Answer {
	addr, _, err := g.at(f.App)
	if err != nil {
		return Answer{App: f.App, Err: err}
	}
	r := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(r)
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	if from != nil {
		from.CopyTo(r)
	}
	r.SetHost(f.App)
	r.Header.SetMethod(f.Method)
	r.URI().Update(path)
	if body != nil {
		r.SetBody(body)
		r.Header.SetContentType("application/json")
	} else {
		r.SetBody(nil)
	}
	if err := clientFor(addr).Do(r, resp); err != nil {
		return Answer{App: f.App, Err: fmt.Errorf("%s at %s: %w", f.App, addr, err)}
	}
	if code := resp.StatusCode(); code < 200 || code > 299 {
		return Answer{App: f.App, Err: fmt.Errorf("%s answered %d for %s %s", f.App, code, f.Method, path)}
	}
	return Answer{App: f.App, Body: append([]byte(nil), resp.Body()...)}
}

// narrow keeps the part of an answer the caller selected. A selection over a list
// applies to each member, which is what a caller means by it.
func narrow(v any, sels []selection) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(sels))
		for _, s := range sels {
			key := s.alias
			if key == "" {
				key = s.name
			}
			got, ok := t[s.name]
			if !ok {
				// Selected and absent is null, not missing: the client asked, and
				// the shape of the answer is what it asked for.
				out[key] = nil
				continue
			}
			if len(s.sels) > 0 {
				out[key] = narrow(got, s.sels)
				continue
			}
			out[key] = got
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, e := range t {
			out = append(out, narrow(e, sels))
		}
		return out
	}
	// A scalar with a selection under it has no parts to take; the value is the
	// answer.
	return v
}
