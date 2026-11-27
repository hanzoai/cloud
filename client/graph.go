package client

// graph.go serves the surface's GraphQL address on zip's engine.
//
// It is the HOST's, for the reason /v1/openapi.json is: the answer is about the
// whole surface, and no plugin can see past itself. Left unclaimed the address
// falls through to whichever app holds the /v1 remainder, which then answers
// with a schema of its OWN registry — one field, honestly rendered, about the
// wrong thing.
//
// What is cloud's here is the SCHEMA and the ANSWER to one root field. The
// schema is openapi.GraphQL over this deployment's composed document, and the
// dispatch is openapi.Fields over the SAME document at the same moment — so a
// field a caller can read is a field a caller can send, by construction rather
// than by two lists agreeing. Everything between them — the parse, variables,
// fragments, directives, the bounds, the projection of each answer through its
// selection — is zip's (zip.App.MountGraphFor), the one GraphQL engine there is.
//
// A ROOT FIELD IS ONE HOP to the app that owns the operation, carrying the
// caller's own request: its credential and cookies, with its identity carried the
// way zip.Call carries one (zip.Identity) rather than copied. That is [ask]'s
// mechanism with the operation's address instead of the app's own: the child
// authenticates the caller, scopes the tenant and answers as itself, exactly as
// it would over REST. Nothing here holds an identity or decides an authorization
// — a field reaches precisely what its REST route reaches, for the caller who
// asked. The root fields of a query are hops in parallel, a few at a time,
// because zip resolves a query's root fields that way.
//
// WHAT A CALLER IS TOLD. A child's refusal is told by its status. Which app
// answered, where it listens and the address it was asked are the deployment's
// own: such an error is not an HTTPError with a 4xx status, so zip logs it under
// the request's id and tells the caller only the id.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

// UseGraph serves the surface schema at path: GET renders it, POST runs a request
// against it.
//
// The schema and the dispatch are built from ONE composed document, once, so the
// fields a caller can read are the fields a caller can send.
//
// Composed lazily and kept, for the reason the index is: composing reads every
// app's embedded subset, and a deployment that never receives a GraphQL request
// should not pay for one.
func UseGraph(app *zip.App, path string, subsets func() ([]openapi.Part, error), at At) {
	build := sync.OnceValues(func() (*surface, error) {
		parts, err := subsets()
		if err != nil {
			return nil, err
		}
		d, err := openapi.Fleet(parts)
		if err != nil {
			return nil, err
		}
		return &surface{resolve: Resolve(d, at), sdl: openapi.GraphQL(d)}, nil
	})
	app.MountGraphFor(path,
		func(ctx context.Context) (string, error) {
			s, err := build()
			if err != nil {
				return "", fmt.Errorf("the surface document would not compose: %w", err)
			}
			return withBase(ctx, at, s.sdl), nil
		},
		func(ctx context.Context, kind string, sel zip.Selection) (any, error) {
			if sel.Name == "base" {
				return delegate(ctx, at, kind, sel)
			}
			s, err := build()
			if err != nil {
				return nil, fmt.Errorf("the surface document would not compose: %w", err)
			}
			return s.resolve(ctx, kind, sel)
		})
}

// surface is the dispatch and its printed SDL, built together from one document
// so they cannot describe different surfaces.
type surface struct {
	resolve zip.Resolver
	sdl     string
}

// Resolve answers the root fields of a composed document's graph: each one is
// the request its operation stands for, sent to the app that owns it.
func Resolve(d *spec.Document, at At) zip.Resolver {
	fields := openapi.Fields(d)
	return func(ctx context.Context, kind string, sel zip.Selection) (any, error) {
		f, ok := fields[sel.Name]
		if !ok {
			return nil, zip.ErrBadRequest(fmt.Sprintf("no field named %q", sel.Name))
		}
		// THE SCHEMA'S SPLIT IS ENFORCED, not only published: a GET is a Query
		// field and nothing else is, so a write cannot be reached by a query —
		// which a cache, a prefetch or a retry may send without a second thought.
		if read := f.Method == http.MethodGet; read != (kind == "query") {
			want := "mutation"
			if read {
				want = "query"
			}
			return nil, zip.ErrBadRequest(fmt.Sprintf("%q is a %s field; this operation is a %s", sel.Name, want, kind))
		}
		path, err := address(f, sel.Args)
		if err != nil {
			return nil, err
		}
		var body []byte
		if raw, sent := sel.Args["body"]; sent {
			if body, err = json.Marshal(raw); err != nil {
				return nil, zip.ErrBadRequest(fmt.Sprintf("body: %v", err))
			}
		}
		answer := send(at, f, path, body, relayed(ctx))
		if answer.Err != nil {
			return nil, answer.Err
		}
		if len(answer.Body) == 0 {
			return nil, nil
		}
		var out any
		if err := json.Unmarshal(answer.Body, &out); err != nil {
			// A child that answered something other than JSON still answered. The
			// bytes are the honest result and hiding them would report a working
			// operation as broken.
			return string(answer.Body), nil
		}
		return out, nil
	}
}

// address turns the operation's path template plus the caller's arguments into
// the address to send. A path parameter with no argument is refused here rather
// than sent as a literal brace the child would 404 on.
func address(f openapi.Field, args map[string]any) (string, error) {
	path := f.Path
	for _, name := range f.Route {
		v, ok := args[name]
		if !ok {
			return "", zip.ErrBadRequest(fmt.Sprintf("%s is part of the address and was not given", name))
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

// own are the headers a hop sets for itself rather than taking from the caller:
// the address and the framing describe THIS request, not the one it relays.
var own = map[string]bool{"Host": true, "Content-Length": true, "Transfer-Encoding": true}

// relayed is the caller's request as a hop carries it: every header, less every
// name ingress strips (ingress.Stripped — the same list, so the two cannot
// drift), plus the identity zip resolves for the caller — the gateway's
// assertion, or an ActingAs — exactly what a zip.Call would carry. So the hop
// does not depend on the edge having stripped a forged header before this ran:
// one it did not mint cannot ride along, and the credential the child
// authenticates by does.
func relayed(ctx context.Context) http.Header {
	h := zip.Headers(ctx)
	for _, name := range principal.Stripped {
		h.Del(name)
	}
	for k, vs := range zip.Identity(ctx) {
		h[k] = vs
	}
	return h
}

// exchange is [ask] with the OPERATION's address instead of the app's own path: the
// same resolution, the same pooled transport, the caller's request headers. The
// child answers as itself for whoever asked.
func exchange(at At, f openapi.Field, path string, body []byte, from http.Header) (int, []byte, error) {
	// A co-resident subsystem is served in memory, the same short-circuit [ask]
	// makes. This hop carries an arbitrary REST address rather than a typed op, so
	// there is no zip.Here to reach for — but a serving app's own router is right
	// here, and replaying the request against it is what the socket was doing.
	local := zip.Serving(f.App)

	addr, _, err := at(f.App)
	if err != nil && local == nil {
		return 0, nil, err
	}
	r := fasthttp.AcquireRequest()
	defer fasthttp.ReleaseRequest(r)
	resp := fasthttp.AcquireResponse()
	defer fasthttp.ReleaseResponse(resp)

	for k, vs := range from {
		if own[http.CanonicalHeaderKey(k)] {
			continue
		}
		for _, v := range vs {
			r.Header.Add(k, v)
		}
	}
	r.SetHost(f.App)
	r.Header.SetMethod(f.Method)
	r.URI().Update(path)
	if body != nil {
		r.SetBody(body)
		r.Header.SetContentType("application/json")
	}
	if local != nil {
		if err := serveHere(local, r, resp); err != nil {
			return 0, nil, fmt.Errorf("%s here: %w", f.App, err)
		}
	} else if err := clientFor(addr).Do(r, resp); err != nil {
		return 0, nil, fmt.Errorf("%s at %s: %w", f.App, addr, err)
	}
	return resp.StatusCode(), append([]byte(nil), resp.Body()...), nil
}

// send is [exchange] answered as a field is told: a child's refusal by its
// status, everything else by the log.
func send(at At, f openapi.Field, path string, body []byte, from http.Header) Answer {
	code, raw, err := exchange(at, f, path, body, from)
	if err != nil {
		return Answer{App: f.App, Err: err}
	}
	if code < 200 || code > 299 {
		if code >= 400 && code < 500 {
			// The child refused the caller, which is the caller's to know; which
			// child, and the address it was asked, are not.
			return Answer{App: f.App, Err: &zip.HTTPError{Status: code,
				Msg: fmt.Sprintf("the operation answered %d %s", code, http.StatusText(code))}}
		}
		return Answer{App: f.App, Err: fmt.Errorf("%s answered %d for %s %s", f.App, code, f.Method, path)}
	}
	return Answer{App: f.App, Body: raw}
}
