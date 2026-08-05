// Package openapi projects the LIVE zip/fiber router into an OpenAPI 3.1
// document. The spec is not a description of the router — it IS the router,
// read through app.Fiber().GetRoutes() at request time. There is no checked-in
// spec file and no second route registry, so the document cannot drift: the only
// way to change it is to change the routes it is read from.
//
// This mirrors the rule zapface/wire.go states for transports — two transports,
// ONE dispatch path. ZAP and OpenAPI are two PROJECTIONS of one route table.
//
// # Reading the router is not a workaround; it is the only total source
//
// Two properties make the LIVE router the only honest source, and no static scan
// of the source tree a substitute:
//
//   - Routes are composed at runtime. POST /v1/kms/auth/login is registered as
//     Group("/v1/kms/auth").Post("/login") — that path literal does not exist
//     anywhere in the tree, so no grep can find it. Only the assembled router
//     knows it.
//   - The route set is a function of DEPLOYMENT CONFIG. Subsystems mount only
//     when cfg.Enabled(name), and several gate routes internally (kms registers
//     its secret routes only `if kc != nil`). The spec therefore VARIES PER
//     DEPLOYMENT, correctly: a deployment that does not mount admin does not
//     advertise admin. That is a feature — each deployment describes itself —
//     and it is why the document is generated per-process, not built once in CI.
//
// # Why not zip's built-in generator
//
// zip ships its own OpenAPI projection (zip/openapi.go) driven by the typed-op
// registry (zip.Get[In,Out] → app.ops), which carries real request/response Go
// types and therefore real JSON Schema. It is the better mechanism and it is NOT
// duplicated here — it is simply empty for this binary: cloud registers ZERO
// typed ops and ~876 plain app.Get/Post handlers, so installOpenAPIRoutes()
// bails at `len(a.ops) == 0` and emits nothing.
//
// GetRoutes() is a strict SUPERSET of app.ops (registerTyped registers a fiber
// route too), so reading the router is the ONE complete source today and stays
// correct as typed ops appear. Migrating a handler to a typed op is what earns
// its schema; see the derivability boundary on Document.
package openapi

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/zap-proto/zip"
)

// Path is the canonical spec endpoint. House law: /v1/ only, no /api/ prefix,
// and never a v2 — the document's own shape is versioned by its `openapi` field.
const Path = "/v1/openapi.json"

// Route is one live route, reduced to what the router actually knows: where a
// request goes. Method and Path are the whole of it.
//
// There is deliberately no handler count here. See Document's note on chained
// handlers for why that number cannot be interpreted.
type Route struct {
	Method string
	Path   string // fiber pattern, e.g. /v1/kms/orgs/:org/secrets/*
}

// methods is the set OpenAPI 3.1 admits as Path Item fields. Both exclusions
// below are FORCED — by representability and by stability — never by taste. A
// method this generator can state stably is emitted even if a CLI would not use
// it (OPTIONS, TRACE); curating that is the consumer's job, not the projection's.
//
// CONNECT is excluded because OpenAPI 3.1 has no `connect` Path Item field.
// Fiber routes it (25 live CONNECT routes come from All() registrations); the
// document format simply cannot express it.
//
// HEAD is excluded because it cannot be stated stably. Fiber auto-generates a
// HEAD for every GET by COPYING the route — same path, same handler stack
// (fiber/router.go:793) — and the `autoHead` flag marking it is unexported, so
// an auto-generated HEAD is indistinguishable from a hand-registered one through
// the public API. Worse, that generation runs in startupProcess() (Listen/Test),
// NOT at registration: the same app yields a different HEAD set before and after
// boot (measured: 25 explicit HEAD routes from All() pre-boot, ~469 post-boot
// once every GET is mirrored). Including HEAD would make the projection depend
// on lifecycle stage — the CI exec path (never listens) and the live endpoint
// (has listened) would emit different documents, which is exactly the drift this
// package exists to prevent. Excluding it makes the projection total and stable.
var methods = map[string]bool{
	"GET": true, "PUT": true, "POST": true, "DELETE": true,
	"OPTIONS": true, "PATCH": true, "TRACE": true,
}

// Live reads the router. It is the SOLE adapter from fiber to data — every other
// function here is a pure function of []Route, so the projection is testable
// without a router and the fiber coupling has exactly one home.
//
// GetRoutes(true) drops Use() middleware entries: middleware matches path
// PREFIXES and is not an operation. That filter is fiber's own (app.go:822), not
// a reimplementation of it.
func Live(app *zip.App) []Route {
	fr := app.Fiber().GetRoutes(true)
	out := make([]Route, 0, len(fr))
	for _, r := range fr {
		if !methods[r.Method] {
			continue
		}
		out = append(out, Route{Method: r.Method, Path: r.Path})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path != out[j].Path {
			return out[i].Path < out[j].Path
		}
		return out[i].Method < out[j].Method
	})
	return out
}

// Product is the product axis, and it is mechanical: the first path segment
// after /v1/ IS the product (/v1/kms/* → kms, /v1/billing/* → billing). No
// judgment, no table to maintain, nothing to keep in sync.
//
// It is deliberately NOT the subsystem name: clients/billing serves both
// /v1/billing/* and /v1/finance/* (finance.go:57), so the mount that owns a
// route and the product a caller names are different values. The CLI wants the
// one in the URL.
//
// Returns "" when the first segment is not a product name — a parameter (:org),
// a wildcard (*), or a file (openapi.json) — and for anything outside /v1
// (/health, /.well-known/*, /git/*, /tasks/*). Those get no tag rather than a
// fabricated one.
func Product(path string) string {
	rest, ok := strings.CutPrefix(path, "/v1/")
	if !ok {
		return ""
	}
	seg, _, _ := strings.Cut(rest, "/")
	if seg == "" || strings.ContainsAny(seg, ":*.+?{}") {
		return ""
	}
	return seg
}

// Info is the OpenAPI info block.
type Info struct {
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version"`
}

// Server is an OpenAPI server entry.
type Server struct {
	URL string `json:"url"`
}

// Tag is an OpenAPI tag — one per product, so a consumer can read the product
// list off the document without walking every path.
type Tag struct {
	Name string `json:"name"`
}

// Schema is the sliver of JSON Schema this generator can honestly assert.
type Schema struct {
	Type string `json:"type"`
}

// Parameter is an OpenAPI parameter object. Only PATH parameters are emitted:
// they are structural — the router matches on them — so they are derivable.
// Query, header, and body parameters are read inside handler bodies the router
// cannot see.
type Parameter struct {
	Name     string `json:"name"`
	In       string `json:"in"`
	Required bool   `json:"required"`
	Schema   Schema `json:"schema"`
}

// Operation is one operation.
//
// There is no Responses field, and that is an honest answer rather than a gap.
// OpenAPI 3.1 makes `responses` OPTIONAL (3.0 required it), so omitting it is
// valid — and a fabricated `200: {description: ok}` on ~900 routes would assert
// a status code and content type this generator has no evidence for. Absent
// beats invented.
type Operation struct {
	OperationID string      `json:"operationId"`
	Tags        []string    `json:"tags,omitempty"`
	Parameters  []Parameter `json:"parameters,omitempty"`
}

// PathItem maps a lowercased HTTP method to its operation.
type PathItem map[string]*Operation

// Document is an OpenAPI 3.1 document.
//
// # What IS derivable from the router, and what is NOT
//
// The router is a dispatch table: pattern → handler. It knows how to MATCH a
// request, not what a request or response CONTAINS. Concretely, fiber's Route
// struct (fiber/v3@v3.2.1 router.go:46) is only {Method, Name, Path, Params,
// Handlers} — there is no payload information in it to read.
//
// Derivable (asserted here, all of it structural):
//   - method — the stack a route is registered in.
//   - path — the registered pattern, verbatim.
//   - path parameters — the pattern's own :name segments; a router that could
//     not name them could not match them. Always required:true (a fiber path
//     param is optional only if written :name?, which cloud does not use).
//   - product tag — the first /v1/ segment of that same pattern.
//
// NOT derivable (absent here; no amount of router-reading changes it):
//   - request body schema. The router holds a func(*zip.Ctx) error. The request
//     type is a LOCAL VARIABLE inside the handler body — clients/kms putSecret
//     is the representative case: `var req secretPutRequest; json.Unmarshal(
//     ctx.Body(), &req)`. The type exists in the package but never appears in
//     the handler's signature, and Go has no reflection from a func value to the
//     types it unmarshals internally. cloud.Handle[S] does not help: its type
//     parameter S is the SERVICE (service.go:90), not the payload. cloud.Typed
//     does not help either: it is an any→*zip.App mount adapter.
//   - response body schema / status codes — same dead end, at the far end.
//   - query and header parameters — read positionally via c.Query("k") at
//     runtime; not part of the match, so the router has never heard of them.
//   - auth requirements — enforced by middleware and by guards wrapped around
//     handlers (guard(s, cloud.Handle(s, listSecrets))), invisible as data.
//   - summaries/descriptions — prose that exists only in Go comments.
//   - wildcard semantics — a fiber `*` matches MULTIPLE segments greedily;
//     OpenAPI's {param} matches one. The emitted {wildcardN} is the closest
//     honest approximation and is NOT equivalent.
//   - whether a route is a real endpoint or a PROXY PREFIX. A catch-all like
//     app.Post("/v1/billing/*") forwards to another service; the operations
//     behind it (POST /v1/billing/deposit and friends) are not routes in this
//     process and cannot appear. For products whose whole surface is one
//     catch-all, this document can name the prefix and nothing under it.
//
// The consequence for a consumer: this document is a complete and exact map of
// the API's SHAPE (every operation, its address, its product) and says nothing
// about payloads. A CLI can build its full command tree — `hanzo <product>
// <resource> <verb>` — and bind path params from it with no judgment calls. It
// cannot typecheck a request body or pretty-print a response from it, and behind
// a catch-all it cannot enumerate subcommands at all.
//
// The path to schemas is not a better reader; it is zip's typed ops
// (zip.Get[In,Out]) which carry the In/Out Go types. Every handler migrated to a
// typed op earns real schema — and an MCP tool, from the same registry (zip's
// THIRD projection, zip/mcp.go). That is a per-handler refactor of business
// logic, not a generator change, and it composes with this: GetRoutes() already
// includes typed ops, so migration adds detail without changing the pipeline.
//
// # Chained handlers are not collisions
//
// Fiber MERGES byte-identical route patterns into ONE Route carrying both
// handlers chained, so a duplicate registration is invisible to an entry count
// and shows only as len(Handlers) > 1. That is true — but the converse does not
// hold, and this generator does NOT use that signal:
//
//	app.Post("/v1/billing/auto-recharge/run-all",
//	    commercemid.RequestContext(), commercemid.TokenRequired(),
//	    commercemid.PlatformOnly(), commercebilling.RunAutoRechargeAllOrgs)
//
// is ONE registration with FOUR handlers — three middleware and a terminal
// handler (apps/commerce.go:151). The whole /v1/store/* surface is the same
// shape. 34 live routes carry chained handlers and every one is legitimate. A
// merged duplicate and a middleware chain are INDISTINGUISHABLE through the
// public API, so "handlers > 1" cannot mean "collision" fleet-wide; treating it
// as one would refuse a spec for a healthy router.
// (clients/bots/routes_test.go asserts exactly that rule, and is right to: it is
// a local truth for the bots/visor/runtime surface, where nothing chains
// middleware. It is not a global one.)
//
// This costs the document nothing. Merged or chained, one pattern is one
// operation — which is what the spec emits either way. Detecting routing bugs is
// the bots guard's job, at the seam where the premise holds; the spec's only
// requirement is that (method, path) → operation stay injective, which From
// enforces via operationId uniqueness.
type Document struct {
	OpenAPI string              `json:"openapi"`
	Info    Info                `json:"info"`
	Servers []Server            `json:"servers,omitempty"`
	Tags    []Tag               `json:"tags,omitempty"`
	Paths   map[string]PathItem `json:"paths"`
}

// From builds the document from route data. Pure — no router, no I/O.
//
// It refuses on a duplicate operationId rather than emit a document a generator
// would mis-consume. That check subsumes the only ambiguity the spec can suffer:
// two routes sharing a (method, path) derive the same id, so an injective
// (method, path) → operation map is exactly what uniqueness buys.
func From(rs []Route, info Info, servers ...Server) (*Document, error) {
	doc := &Document{
		OpenAPI: "3.1.0",
		Info:    info,
		Servers: servers,
		Paths:   map[string]PathItem{},
	}

	products := map[string]bool{}
	opIDs := map[string]string{} // operationId → "METHOD path", for the clash message

	for _, r := range rs {
		path, params := translate(r.Path)

		op := &Operation{OperationID: operationID(r.Method, path)}
		if prev, dup := opIDs[op.OperationID]; dup {
			return nil, fmt.Errorf("operationId %q is claimed by both %q and %q — OpenAPI requires it unique",
				op.OperationID, prev, r.Method+" "+r.Path)
		}
		opIDs[op.OperationID] = r.Method + " " + r.Path

		if p := Product(r.Path); p != "" {
			op.Tags = []string{p}
			products[p] = true
		}
		for _, name := range params {
			op.Parameters = append(op.Parameters, Parameter{
				Name: name, In: "path", Required: true, Schema: Schema{Type: "string"},
			})
		}

		if doc.Paths[path] == nil {
			doc.Paths[path] = PathItem{}
		}
		doc.Paths[path][strings.ToLower(r.Method)] = op
	}

	for p := range products {
		doc.Tags = append(doc.Tags, Tag{Name: p})
	}
	sort.Slice(doc.Tags, func(i, j int) bool { return doc.Tags[i].Name < doc.Tags[j].Name })

	return doc, nil
}

// Spec reads the live router and projects it — Live + From, the one call a
// caller wants.
func Spec(app *zip.App, info Info, servers ...Server) (*Document, error) {
	return From(Live(app), info, servers...)
}

// translate rewrites a fiber pattern into an OpenAPI path template and returns
// the parameter names in path order.
//
// fiber :name → OpenAPI {name}. A fiber wildcard (* or +) has no OpenAPI
// equivalent; it becomes {wildcardN}, numbered in path order. fiber's own key
// for it is "*1", which is not a legal URI-template name, so it cannot be
// reused verbatim.
func translate(pattern string) (string, []string) {
	segs := strings.Split(pattern, "/")
	var params []string
	stars := 0
	for i, s := range segs {
		switch {
		case strings.HasPrefix(s, ":"):
			name := strings.TrimSuffix(strings.TrimPrefix(s, ":"), "?")
			segs[i] = "{" + name + "}"
			params = append(params, name)
		case s == "*" || s == "+":
			stars++
			name := fmt.Sprintf("wildcard%d", stars)
			segs[i] = "{" + name + "}"
			params = append(params, name)
		}
	}
	return strings.Join(segs, "/"), params
}

// operationID derives a stable id from method+path.
//
// '_' is the SEPARATOR (it encodes '/'), so any character that also folded to
// '_' would collide with a path boundary. That is not hypothetical: the live
// router serves both GET /v1/pricing-policy and GET /v1/pricing/policy, which an
// earlier "everything non-alphanumeric → _" rule collapsed onto one id. '-' and
// '.' are legal in an operationId and are therefore preserved rather than
// folded, which keeps that pair distinct (get_v1_pricing-policy vs
// get_v1_pricing_policy).
//
// Params contribute "by_<name>" so /v1/a/{b} and /v1/a/b do not collapse either.
// This is derivation, not proof: a literal '_' in a segment can still alias a
// '/' (/v1/a/b_c vs /v1/a/b/c). From VERIFIES uniqueness over the whole document
// and fails loudly rather than emit a duplicate — the guard, not the encoding,
// is what makes the ids trustworthy.
func operationID(method, path string) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(method))
	for _, s := range strings.Split(path, "/") {
		if s == "" {
			continue
		}
		b.WriteByte('_')
		if strings.HasPrefix(s, "{") {
			b.WriteString("by_")
			s = strings.TrimSuffix(strings.TrimPrefix(s, "{"), "}")
		}
		b.WriteString(sanitize(s))
	}
	return b.String()
}

// sanitize reduces a path segment to [a-z0-9.-], the characters that are legal
// in an operationId and cannot be confused with the '_' path separator.
func sanitize(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// Mount serves the document at Path off app's OWN live router — the app it is
// registered on is the app it reads, so what it serves is that process's actual
// route table and nothing else.
//
// It is UNAUTHENTICATED, deliberately:
//
//   - It is an API description of a public API, and it grants no capability.
//     Every route it names stays individually auth-gated; reading the map does
//     not open a door. Withholding it would be obscurity, not access control.
//   - The `hanzo` CLI must build its command tree BEFORE a user logs in. Gating
//     the spec would make `hanzo --help` require credentials.
//   - It carries no schemas, no examples, no secrets — only addresses.
//   - Enablement already scopes it: the document is generated from what THIS
//     deployment mounted, so a deployment that does not enable admin does not
//     list admin routes. The blast radius is the deployment's own surface.
//
// The cost accepted is path enumeration on a deployment that mounts admin. That
// is a recon convenience, not an authorization change. If a deployment ever
// needs it closed, the lever is a guard here — one line, one place.
//
// The document is built once, lazily, on first request: the route table is fixed
// after boot, and building lazily (rather than at Mount) means it includes every
// route — including this one, and any registered after Mount.
func Mount(app *zip.App, info Info, servers ...Server) {
	var (
		once sync.Once
		doc  *Document
		err  error
	)
	app.Get(Path, func(c *zip.Ctx) error {
		once.Do(func() { doc, err = Spec(app, info, servers...) })
		if err != nil {
			return zip.ErrInternal(err.Error())
		}
		return c.JSON(200, doc)
	})
}
