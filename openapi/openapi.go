// Package openapi projects the LIVE zip/fiber router into an OpenAPI 3.1
// document. The spec is not a description of the router — it IS the router,
// read through app.Fiber().GetRoutes() at request time. There is no checked-in
// spec file and no second route registry, so the document cannot drift: the only
// way to change it is to change the routes it is read from. (Register adds a
// registry of BODIES, never of routes: a declared schema renders only on a route
// the router carries, so the paths remain the router's alone.)
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
// # Two readings of one router, folded into one document
//
// zip ships its own OpenAPI projection (zip/openapi.go) driven by the typed-op
// registry (zip.Get[In, Out] → app.ops), which carries the real request/response
// Go types and therefore real JSON Schema — plus the prose cmd/zipdoc lifts out
// of the handlers' doc comments at build time. It is the better reading, and it
// is not duplicated here: Typed READS it and Fold lays it over the router
// projection.
//
// The two are not rivals because GetRoutes() is a strict SUPERSET of app.ops
// (registerTyped registers a fiber route too). So the router gives the TOTAL set
// of operations and the registry gives DETAIL for the subset that has any:
//
//	router   → every operation exists, with its address and product.
//	registry → the typed ones also carry schemas, parameters, responses,
//	           descriptions and examples.
//
// One document, no gaps and no invention. Migrating a raw handler to a typed op
// is what earns it the second half, and it needs no generator change: the same
// fold picks it up on the next run.
package openapi

import (
	"encoding/json"
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

// Schema is the sliver of JSON Schema this generator can honestly assert as a
// CLOSED struct: primitive types for path parameters, and — for routes whose
// subsystem declared its bodies via Register — objects, arrays, and $refs
// derived by reflection from the handler's own binding structs. Typed-op
// schemas do NOT pass through it: zip already derives arbitrary JSON Schema
// from the In/Out Go types, and restating that open vocabulary here would be a
// second, lossier copy — they travel as `any` (see Fold).
// Format qualifies Type where the type alone is not the whole fact. It carries
// exactly one value today — "binary" on the string that stands for a raw byte
// body ([Binary]) — because that is the only distinction this generator can make
// that a consumer acts on: an SDK generator emits a file/bytes parameter for
// `string/binary` and a text parameter for a bare `string`.
type Schema struct {
	Type                 string             `json:"type,omitempty"`
	Format               string             `json:"format,omitempty"`
	Ref                  string             `json:"$ref,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	AdditionalProperties *Schema            `json:"additionalProperties,omitempty"`
}

// Parameter is an OpenAPI parameter object.
//
// From the ROUTER only path parameters are emitted: they are structural — the
// router matches on them — so they are derivable. From the REGISTRY a typed op's
// query parameters come too, with descriptions, because its input type says what
// they are. Schema is an open map because JSON Schema is an open vocabulary: the
// router asserts `{"type": "string"}` and nothing more, while a typed op's field
// can be any shape zip derives from its Go type.
//
// Example is that field's value in the op's own example. A BODYLESS op — every
// GET and, since zip v1.18.0, every DELETE — has no requestBody for the doc
// comment's Example to live in, so zip splits it across the parameters that
// carry it. Without a name for it here the round-trip through [Typed] dropped
// it, and every GET and DELETE reached the published reference with no example
// at all.
type Parameter struct {
	Name        string          `json:"name"`
	In          string          `json:"in"`
	Required    bool            `json:"required"`
	Description string          `json:"description,omitempty"`
	Schema      map[string]any  `json:"schema,omitempty"`
	Example     json.RawMessage `json:"example,omitempty"`
}

// Operation is one operation.
//
// Everything past Parameters is omitempty and comes from exactly two seams, both
// anchored in the handler's own Go types so neither can drift:
//
//   - Register (register.go): a subsystem declares its binding structs for a
//     route it already serves — schema by reflection, attached only when the
//     router carries the route.
//   - zip's typed ops, folded in from the registry (Fold): Summary/Description
//     from the lifted godoc, query parameters and bodies from the In/Out types.
//
// A route neither seam knows keeps exactly the structural facts the router can
// prove, and asserts no status code, content type or prose it has no evidence
// for — OpenAPI 3.1 makes `responses` OPTIONAL (3.0 required it), so absent
// stays valid and absent beats invented.
//
// RequestBody and Responses are `any` because the two seams produce different
// (JSON-identical) shapes: Register builds the closed *RequestBody /
// map[string]*Response, the fold reuses zip's open maps verbatim.
type Operation struct {
	OperationID string      `json:"operationId"`
	Summary     string      `json:"summary,omitempty"`
	Description string      `json:"description,omitempty"`
	Tags        []string    `json:"tags,omitempty"`
	Parameters  []Parameter `json:"parameters,omitempty"`
	RequestBody any         `json:"requestBody,omitempty"`
	Responses   any         `json:"responses,omitempty"`
}

// Components holds the named schemas operations reference by $ref, so an SDK
// generator mints one named type per Go struct instead of an anonymous shape per
// operation. Open-typed: Register contributes *Schema values, the typed fold
// contributes zip's JSON Schema verbatim — both marshal to the same vocabulary.
type Components struct {
	Schemas map[string]any `json:"schemas,omitempty"`
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
// NOT derivable from the router (no amount of router-reading changes it — these
// come from the typed registry via Fold, and are absent on a route that has no
// typed op):
//   - request body schema. The router holds a func(*zip.Ctx) error. The request
//     type is a LOCAL VARIABLE inside the handler body — clients/kms putSecret
//     is the representative case: `var req secretPutRequest; json.Unmarshal(
//     ctx.Body(), &req)`. The type exists in the package but never appears in
//     the handler's signature, and Go has no reflection from a func value to the
//     types it unmarshals internally. cloud.Handle[S] does not help: its type
//     parameter S is the SERVICE (service.go:90), not the payload. cloud.Typed
//     does not help either: it is an any→*zip.App mount adapter.
//     What cannot be DERIVED can still be DECLARED: Register (register.go) is
//     the seam a subsystem uses to state its binding structs once, next to its
//     route table, and the schema is reflected from those structs.
//   - response body schema / status codes — same dead end, at the far end,
//     with the same declaration seam (the success shape under a "2XX" range,
//     because the exact code lives in the handler body).
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
// the API's SHAPE (every operation, its address, its product), and it describes
// PAYLOADS exactly as far as the typed registry does. A CLI can build its full
// command tree — `hanzo <product> <resource> <verb>` — and bind path params from
// it with no judgment calls, everywhere. It can typecheck a request body and
// pretty-print a response only for a typed op, and behind a catch-all it cannot
// enumerate subcommands at all.
//
// The path to more schema is not a better reader. Two seams exist, both
// anchored in the handler's own Go types so neither can drift:
//
//   - Register (register.go): a subsystem declares its binding structs for a
//     route it already serves — no handler change, schema by reflection. A
//     registration renders ONLY when the router carries the route, so the
//     document still cannot disagree with the router; schemas are additive
//     metadata on routes that exist.
//   - zip's typed ops (zip.Get[In,Out]), which carry the In/Out types in the
//     handler signature itself and also earn an MCP tool and a CLI command from
//     the same registry entry (zip's third projection, zip/mcp.go). That is a
//     per-handler refactor of business logic, and it composes with this:
//     GetRoutes() already includes typed ops, so migration adds detail through
//     the fold that is already running.
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
	OpenAPI    string              `json:"openapi"`
	Info       Info                `json:"info"`
	Servers    []Server            `json:"servers,omitempty"`
	Tags       []Tag               `json:"tags,omitempty"`
	Paths      map[string]PathItem `json:"paths"`
	Components *Components         `json:"components,omitempty"`
}

// From builds the document from route data. No router, no I/O — its inputs are
// the routes, plus the package registry of declared bodies (Register), which is
// written only at init time and is therefore fixed by the time any document is
// built.
//
// It refuses on a duplicate operationId rather than emit a document a generator
// would mis-consume. That check subsumes the only ambiguity the spec can suffer:
// two routes sharing a (method, path) derive the same id, so an injective
// (method, path) → operation map is exactly what uniqueness buys. It refuses
// equally when two DIFFERENT Go types claim one component name — a silent merge
// would hand an SDK generator a lie.
func From(rs []Route, info Info, servers ...Server) (*Document, error) {
	doc := &Document{
		OpenAPI: "3.1.0",
		Info:    info,
		Servers: servers,
		Paths:   map[string]PathItem{},
	}

	products := map[string]bool{}
	comp := newComponents()

	for _, r := range rs {
		path, params := translate(r.Path)

		op := &Operation{OperationID: operationID(r.Method, path)}
		if p := Product(r.Path); p != "" {
			op.Tags = []string{p}
			products[p] = true
		}
		for _, name := range params {
			op.Parameters = append(op.Parameters, Parameter{
				Name: name, In: "path", Required: true, Schema: map[string]any{"type": "string"},
			})
		}

		// Declared bodies attach ONLY here — to a route the router carries. A
		// registration without a live route never renders, which is what keeps
		// the registry unable to contradict the router.
		if reg := registered(r.Method, r.Path); reg != nil {
			if err := reg.apply(op, comp); err != nil {
				return nil, err
			}
		}

		if doc.Paths[path] == nil {
			doc.Paths[path] = PathItem{}
		}
		doc.Paths[path][strings.ToLower(r.Method)] = op
	}
	if len(comp.schemas) > 0 {
		doc.Components = &Components{Schemas: comp.schemas}
	}

	for p := range products {
		doc.Tags = append(doc.Tags, Tag{Name: p})
	}
	sort.Slice(doc.Tags, func(i, j int) bool { return doc.Tags[i].Name < doc.Tags[j].Name })

	if err := uniqueOperationIDs(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

// Registry is zip's typed-op projection, reduced to what the router cannot
// supply: the operations that carry schema and prose, keyed by the same
// "METHOD /templated/path" identity a Document is indexed by, and the schemas
// they $ref.
type Registry struct {
	Ops     map[string]*Operation
	Schemas map[string]any
}

// Typed reads the typed-op registry off the app — the SAME value zip serves at
// /.well-known/openapi.json, so there is one generator for schemas and this is
// not it.
//
// It comes back through JSON rather than by walking zip's map[string]any because
// the map IS a JSON document; decoding it into the same Operation the router
// projection builds is what makes the two foldable at all, and it means a field
// zip adds later arrives here without a change (or is dropped honestly, if
// nothing here has a name for it).
func Typed(app *zip.App) (Registry, error) {
	raw, err := json.Marshal(app.OpenAPISpec())
	if err != nil {
		return Registry{}, fmt.Errorf("typed registry: %w", err)
	}
	var spec struct {
		Paths      map[string]map[string]*Operation `json:"paths"`
		Components Components                       `json:"components"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return Registry{}, fmt.Errorf("typed registry: %w", err)
	}
	reg := Registry{Ops: map[string]*Operation{}, Schemas: spec.Components.Schemas}
	for path, item := range spec.Paths {
		for method, op := range item {
			reg.Ops[strings.ToUpper(method)+" "+path] = op
		}
	}
	return reg, nil
}

// Fold lays the registry's detail over the router's shape, in place. A typed op
// REPLACES the structural operation at its address — it is strictly richer,
// including the path parameters, which zip derives from the same pattern.
//
// Two things the fold keeps from the router, both because the router is the
// authority on them:
//
//   - the product tag, which is the path's first /v1/ segment and nothing else.
//     zip's per-op tags are a different axis and cloud registers none.
//   - membership. A typed op with no live route is a contradiction — registering
//     one registers a fiber route — so it means the two readings disagree about
//     a path (a translation bug), and the honest answer is to refuse rather than
//     to invent the operation or drop it silently.
func Fold(doc *Document, reg Registry) error {
	for _, key := range sortedKeys(reg.Ops) {
		op := reg.Ops[key]
		method, path, _ := strings.Cut(key, " ")
		shape := doc.Paths[path][strings.ToLower(method)]
		if shape == nil {
			return fmt.Errorf("typed op %q has no live route — the registry and the router disagree about its path", key)
		}
		op.Tags = shape.Tags
		doc.Paths[path][strings.ToLower(method)] = op
	}
	// MERGE, never replace: From may already have named schemas from Register.
	// On a name both seams claim, the typed op wins — its schema is derived from
	// the handler signature itself, the strongest evidence there is — and the
	// override is deterministic (sorted key order above, single writer here).
	if len(reg.Schemas) > 0 {
		if doc.Components == nil {
			doc.Components = &Components{Schemas: map[string]any{}}
		}
		for _, name := range sortedKeys(reg.Schemas) {
			doc.Components.Schemas[name] = reg.Schemas[name]
		}
	}
	return uniqueOperationIDs(doc)
}

// Spec reads the live router and both projections of it — the one call a caller
// wants.
func Spec(app *zip.App, info Info, servers ...Server) (*Document, error) {
	doc, err := From(Live(app), info, servers...)
	if err != nil {
		return nil, err
	}
	reg, err := Typed(app)
	if err != nil {
		return nil, err
	}
	if err := Fold(doc, reg); err != nil {
		return nil, err
	}
	return doc, nil
}

// uniqueOperationIDs refuses a document a generator would mis-consume. It runs
// after BOTH projections because either can name an operation: From derives an
// id from method+path, Fold takes the registry's own (WithOperationID), and a
// clash between the two families is exactly as broken as one within either.
//
// The check subsumes the only ambiguity the document can suffer: two routes
// sharing a (method, path) derive the same id, so an injective
// (method, path) → operation map is what uniqueness buys.
func uniqueOperationIDs(d *Document) error {
	seen := map[string]string{} // operationId → "METHOD path", for the clash message
	for _, path := range sortedKeys(d.Paths) {
		item := d.Paths[path]
		for _, method := range sortedKeys(item) {
			at := strings.ToUpper(method) + " " + path
			if prev, dup := seen[item[method].OperationID]; dup {
				return fmt.Errorf("operationId %q is claimed by both %q and %q — OpenAPI requires it unique",
					item[method].OperationID, prev, at)
			}
			seen[item[method].OperationID] = at
		}
	}
	return nil
}

// sortedKeys makes map iteration deterministic, so a clash reports the same pair
// every run.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
