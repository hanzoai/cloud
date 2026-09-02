// Package openapi projects the LIVE zip/fiber router into an OpenAPI 3.1
// document. The spec is not a description of the router — it IS the router,
// read through app.Fiber().GetRoutes(). There is no second route registry, so a
// document and the app that produced it cannot drift: the only way to change it
// is to change the routes it is read from. (Register/Describe add a registry of
// BODIES and PROSE, never of routes: a declaration renders only on a route the
// router carries, so the paths remain the router's alone.)
//
// THAT GUARANTEE IS PER APP, AND IT ENDS AT THE APP. [Spec] and [Mount] read a
// router that is right there. The FLEET document — what api.hanzo.ai serves —
// cannot: the light host mounts no subsystem, so [UseFleet] composes the
// projections 116 app binaries wrote when they were BUILT (fleet.go). Between the
// projection and the request sit two gaps no reading of any router closes: the
// subset can be older than the code (mk/fleet.mk check regenerates it
// from source and refuses the diff), and the deployed edge can hand the
// path to somebody else entirely (only a probe of the live host sees that). See
// fleetInfo for what the published document may therefore claim.
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
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/manifest/mcp"
	"github.com/zap-proto/zip"
)

// Path is the canonical spec endpoint. House law: /v1/ only, no /api/ prefix,
// and never a v2 — the document's own shape is versioned by its `openapi` field.
const Path = "/v1/openapi.json"

// GraphPath is the same surface as a QUERY LANGUAGE. Not /v1/graph: that is the
// knowledge graph's own address — assertions, entities, neighbours — and the two
// mean different things by the word.
const GraphPath = "/v1/graphql"

// WellKnown is where a client that has never seen this API looks first. RFC 8615
// reserves /.well-known/ for exactly that, so every generator, IDE and crawler
// probes it before it probes anything of ours.
//
// It is an ALIAS, not a second document: [serve] answers both from the one lazy
// render, so the two addresses cannot describe different APIs. Path stays
// canonical and is what the spec's own self-description names.
//
// THE ADDRESS IS ZIP'S, and so is the name — spelling the string here a second
// time is how two copies of one fact come to disagree after a framework bump.
//
// It also already ANSWERS, which is the whole reason this exists. zip auto-mounts
// a document of its own there from its own typed-op registry ([zip.App.Registry]),
// installed at Serve. On the light host that registry is almost empty, so what
// api.hanzo.ai publishes at the address every generator probes is an 841-byte
// document titled "cloud 0.0.0" describing /healthz and /readyz — measured, and
// it is what /docs renders too. A generator reading it produces an empty client
// AND REPORTS SUCCESS; a 404 would at least be honest.
//
// Registering here WINS rather than collides: zip's projections are control
// routes, materialised after every ordinary route (zip build.go), and fiber
// resolves a duplicate pattern by first registration. So this is not a second
// answer added beside zip's — it is the one answer, in front of a weaker one
// that stops being reachable.
const WellKnown = zip.SpecPath

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
// TRACE and OPTIONS are excluded for the reason CONNECT and HEAD are: All()
// binds them mechanically, nobody DECLARED them, and a published operation is a
// promise. They are transport, not product.
//
// TRACE echoes the request back. It is a debugging verb, it is the Cross-Site
// Tracing vector, and it should not be reachable on a public API at all — so
// advertising 33 of them in the contract is worse than merely routing them,
// because the contract is what SDKs, the CLI and the MCP tool list are built
// from. OPTIONS is CORS preflight: a browser sends it, a person never does, and
// `hanzo meet options` is not a command anyone wants.
//
// Between them they were 66 of the document's operations — 66 SDK methods, 66
// CLI verbs and 66 MCP tools that existed because a route table was read as a
// product surface.
var methods = map[string]bool{
	"GET": true, "PUT": true, "POST": true, "DELETE": true, "PATCH": true,
}

// Methods returns the methods this generator publishes, sorted.
//
// It exists for ONE caller shape: a subsystem whose whole surface is an All()
// registration. All() binds every method at one path, so there is no per-method
// registration site to hang prose on — the subsystem declares its prose in a loop
// instead, and that loop has to cover EXACTLY what the document renders. Reading
// the projection's own set is what makes it exact: a method added or dropped here
// moves both halves at once, so the loop can neither describe an operation nobody
// publishes nor miss one that is published.
func Methods() []string {
	out := make([]string, 0, len(methods))
	for m := range methods {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
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
		// The router is a superset of the contract: it carries zip's own
		// control routes and anything the app serves without declaring — a
		// retired address answers 410 for every method and is not something a
		// caller can DO, so it is not an operation.
		if !app.Declares(r.Method, r.Path) {
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
// It is deliberately NOT the subsystem name: commerce serves the merchant half
// of /v1/billing/* while apps/billing serves the customer's own reads there, so
// the mount that owns a route and the product a caller names are different
// values. The CLI wants the one in the URL.
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
//
// Description says what the product IS, in the words of the package that
// implements it (Synopsis). It is omitted rather than filled: a product whose
// package carries no doc comment is a product nobody has described yet, and
// inventing a sentence here would make that indistinguishable from one somebody
// wrote. The NAME is never conditional on it — the tag list stays a function of
// the document's operations, which is the one thing a consumer enumerating
// products can rely on.
type Tag struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
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
// OneOf carries the alternatives of a POLYMORPHIC body ([OneOf] the declaration
// value) and is empty on every other schema. It is the one place this generator
// says "several shapes, and the caller picks" — a fact a single Go type cannot
// state, which is why declaring one alternative and calling it the wire would
// under-describe a route that accepts three.
type Schema struct {
	Type                 string             `json:"type,omitempty"`
	Format               string             `json:"format,omitempty"`
	Ref                  string             `json:"$ref,omitempty"`
	Items                *Schema            `json:"items,omitempty"`
	Properties           map[string]*Schema `json:"properties,omitempty"`
	AdditionalProperties *Schema            `json:"additionalProperties,omitempty"`
	OneOf                []*Schema          `json:"oneOf,omitempty"`
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
// Everything past Parameters is omitempty and comes from exactly two clients, both
// anchored in the handler's own Go types so neither can drift:
//
//   - Register/Describe (register.go): a subsystem declares its binding structs
//     — schema by reflection — and, for a handler the wire refuses to let become
//     a typed op, its prose; both attach only when the router carries the route.
//   - zip's typed ops, folded in from the registry (Fold): Summary/Description
//     from the lifted godoc, query parameters and bodies from the In/Out types.
//
// A route neither client knows keeps exactly the structural facts the router can
// prove, and asserts no status code, content type or prose it has no evidence
// for — OpenAPI 3.1 makes `responses` OPTIONAL (3.0 required it), so absent
// stays valid and absent beats invented.
//
// RequestBody and Responses are `any` because the two clients produce different
// (JSON-identical) shapes: Register builds the closed *RequestBody /
// map[string]*Response, the fold reuses zip's open maps verbatim.
// App is PROVENANCE: the registry that registered this operation. It is the one
// question a reader of a wrong operation has — who do I file this against — and
// until it was recorded the answer took a bisect of 116 subsets. For an app's own
// route it is the app name, so the code is apps/<name> in this repo; for an
// operation reached through a relay it is the module behind that relay, so the code
// is that repo. Either way it names a place to go.
//
// It is written exactly once per operation, by whichever producer knows: [Project]
// stamps the relay's source as the operation enters, [Compose] stamps the part's app
// for everything else, and neither overwrites a value already there.
//
// Public is AUDIENCE: this operation is part of the customer contract. It is
// derived from the operation's own address and tags at the end of [Spec] — see
// openapi/public.go for the rule, and [Publish] for the projection that reads
// it. It rides in the document as an extension rather than as a tag because the
// tag axis already means PRODUCT — `compat` had to be filtered back out of it by
// [Products], and a second orthogonal fact in the same slot would be that wart
// twice.
// Security overrides the document-level requirement for ONE operation, and a
// pointer is what lets it say the two different things OpenAPI distinguishes:
// absent means "inherit the document's requirement" and present-and-empty means
// "this needs no credential at all". A plain slice collapses both onto nil and
// could only ever say the first. Nothing sets it to a non-empty value — an
// operation with a requirement of its own would be a second credential, and there
// is one (openapi/security.go). [Open] is what writes it.
type Operation struct {
	OperationID string         `json:"operationId"`
	Summary     string         `json:"summary,omitempty"`
	Description string         `json:"description,omitempty"`
	Tags        []string       `json:"tags,omitempty"`
	Parameters  []Parameter    `json:"parameters,omitempty"`
	RequestBody any            `json:"requestBody,omitempty"`
	Responses   any            `json:"responses,omitempty"`
	Security    *[]Requirement `json:"security,omitempty"`
	App         string         `json:"x-app,omitempty"`
	Public      bool           `json:"x-public,omitempty"`
	// Stage is whether a customer is shown the capability that serves this
	// operation: "beta", "alpha", or ABSENT for ga (HIP-0139 §8).
	//
	// Absent means ga for the same reason the manifest's empty row does: ga is the
	// default and a third spelling of it would be a second way to say one thing.
	// It is stamped by [Compose], from the serving app's manifest row, because the
	// row is the one place the fact is declared and the compose is the first point
	// at which a document knows whose app an operation came from — an app
	// describing itself does not read the fleet's manifest and must not start
	// guessing its own stage.
	//
	// An extension rather than a tag, exactly as [Operation.Public] and
	// [Operation.Tool] are: the tag axis means the capability, and a generator
	// ignores an `x-` it does not know.
	Stage string `json:"x-stage,omitempty"`
	// Tool says this operation is DISPATCHABLE, not merely described.
	//
	// zip builds its MCP tool list by walking the typed registry and nothing else
	// (mcpTools → Registry), so an op earns a tool exactly when it is typed —
	// which is the same set [Fold] folds, and why this is written there and
	// nowhere else. An untyped route reaches the document through [From] and a
	// declared one through [Register]; both describe a shape the child's registry
	// has never heard of, so both leave this false.
	//
	// It exists because the fleet's agent MCP server reads a CATALOG built from these
	// documents, and a document carries every ROUTE. Advertising the difference
	// is not a cosmetic over-count: a caller that picks one of those names gets
	// `unknown tool` from the child that published it — measured on the live server,
	// `storage`/`list_s3_buckets`, and 157 operations across ten apps behind it.
	// Marking the dispatchable subset here lets the catalog carry what a child
	// ANSWERS TO rather than what it describes, which is what Op's own doc comment
	// in fleet/catalog.go has always claimed it holds.
	//
	// An extension rather than a tag: the tag axis means PRODUCT, and a generator
	// ignores an unknown `x-`. It is derived on every regeneration and never
	// hand-set — the same law Register, Describe and Public are held to.
	Tool bool `json:"x-tool,omitempty"`
}

// Components holds the named schemas operations reference by $ref, so an SDK
// generator mints one named type per Go struct instead of an anonymous shape per
// operation. Open-typed: Register contributes *Schema values, the typed fold
// contributes zip's JSON Schema verbatim — both marshal to the same vocabulary.
//
// SecuritySchemes names the credential the API reads. It is the one component a
// route table cannot supply, so it is declared rather than derived — see
// openapi/security.go, and [Document.Security] for the requirement that points at
// it.
type Components struct {
	Schemas         map[string]any            `json:"schemas,omitempty"`
	SecuritySchemes map[string]SecurityScheme `json:"securitySchemes,omitempty"`
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
//     the client a subsystem uses to state its binding structs once, next to its
//     route table, and the schema is reflected from those structs.
//   - response body schema / status codes — same dead end, at the far end,
//     with the same declaration client (the success shape under a "2XX" range,
//     because the exact code lives in the handler body).
//   - query and header parameters — read positionally via c.Query("k") at
//     runtime; not part of the match, so the router has never heard of them.
//   - auth requirements — enforced by middleware and by guards wrapped around
//     handlers (guard(s, cloud.Handle(s, listSecrets))), invisible as data. No
//     reading of a func value recovers them, so the credential is DECLARED
//     (openapi/security.go): one scheme, a document-level requirement every
//     operation inherits, and [Open] on the few that need none.
//   - summaries/descriptions — prose that exists only in Go comments (a typed
//     op's zipdoc lift, or a refused route's Describe declaration).
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
// The path to more schema is not a better reader. Two clients exist, both
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
//	app.Post("/v1/billing/recharge/run-all",
//	    commercemid.RequestContext(), commercemid.TokenRequired(),
//	    commercemid.PlatformOnly(), commercebilling.RunAutoRechargeAllOrgs)
//
// is ONE registration with FOUR handlers — three middleware and a terminal
// handler (apps/commerce/mount.go). The whole /v1/commerce/store/* surface is the same
// shape. 34 live routes carry chained handlers and every one is legitimate. A
// merged duplicate and a middleware chain are INDISTINGUISHABLE through the
// public API, so "handlers > 1" cannot mean "collision" fleet-wide; treating it
// as one would refuse a spec for a healthy router.
// (apps/bot/routes_test.go asserts exactly that rule, and is right to: it is
// a local truth for the bots/visor/runtime surface, where nothing chains
// middleware. It is not a global one.)
//
// This costs the document nothing. Merged or chained, one pattern is one
// operation — which is what the spec emits either way. Detecting routing bugs is
// the bots guard's job, at the client where the premise holds; the spec's only
// requirement is that (method, path) → operation stay injective, which From
// enforces via operationId uniqueness.
type Document struct {
	OpenAPI    string              `json:"openapi"`
	Info       Info                `json:"info"`
	Servers    []Server            `json:"servers,omitempty"`
	Security   []Requirement       `json:"security,omitempty"`
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

		op := &Operation{OperationID: zip.ID(r.Method, path)}
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
		// An UNTYPED route inside a COMPOSED app has its prose in neither place a
		// host can reach: not in the typed registry (it is not typed) and not in
		// [Describe] (that is this repo's client, and the route is another repo's).
		// It is in the doc comment on the handler, where it belongs, and zipdoc
		// put it in zip's process-wide extraction — so ask there.
		//
		// Last, so nothing here overrides prose this app declared for itself: a
		// host that has something to add about a route it mounts still wins, and
		// the owning service's sentence is what fills the silence.
		if op.Summary == "" && op.Description == "" {
			if summary, description, ok := zip.Prose(r.Method, r.Path); ok {
				op.Summary, op.Description = summary, description
			}
		}

		if doc.Paths[path] == nil {
			doc.Paths[path] = PathItem{}
		}
		doc.Paths[path][strings.ToLower(r.Method)] = op
	}
	// A relayed document's own schemas, beside the ones reflected from Go types
	// here. Only where this app has not already claimed the name: a Go type in
	// this binary is the more specific claim, exactly as it is for a response.
	// A collision ACROSS apps is the fleet's business and the noun gate's.
	regMu.Lock()
	for name, schema := range relayed {
		if _, claimed := comp.schemas[name]; !claimed {
			comp.schemas[name] = schema
		}
	}
	regMu.Unlock()
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

// Compat marks an operation whose ADDRESS is a legacy spelling of another
// operation's: served so a consumer pinned to it does not break, and not part of
// the contract.
//
// The router cannot know this. `/v1/iam/get-users` and `/v1/iam/users` are two
// live routes with no relation the route table can see; only the code that
// registers them knows one replaced the other. So the SERVING code declares it —
// hanzoai/iam tags the fifty-one entity verbs it inherited (`get-users`,
// `add-application`, `set-preferred-mfa`) and the singular `application` address
// it had before the kind was pluralized — and the declaration rides the compose
// out to hanzoai/openapi, whose merge keeps those addresses out of the published
// document. Without it the customer surface carries two spellings of every one of
// those operations: two SDK methods, two docs entries, two `hanzo iam` commands.
//
// It is a SECOND tag, never the first: the product tag is the axis every
// generator files an operation under, and this is an orthogonal fact about the
// same operation.
const Compat = "compat"

// Products drops Compat from an operation's tag list.
//
// Compat is an orthogonal fact about ONE address, not a product. Everything that
// reads tags to answer "which products does this document publish" — the floor
// ratchet, the compose's ownership claims, the document's own tag list — has to
// ask this instead of iterating Tags, or `compat` arrives as a product with
// twenty-three operations in it and a doc-site heading nothing answers to.
func Products(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if t != Compat {
			out = append(out, t)
		}
	}
	return out
}

// Fold lays the registry's detail over the router's shape, in place. A typed op
// REPLACES the structural operation at its address — it is strictly richer,
// including the path parameters, which zip derives from the same pattern.
//
// Two things the fold keeps from the router, both because the router is the
// authority on them:
//
//   - the product tag, which is the path's first /v1/ segment and nothing else.
//     zip's per-op tags name a different axis, and the one that carries a fact
//     the router cannot know survives beside it (Compat, below).
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
		legacy := slices.Contains(op.Tags, Compat)
		op.Tags = shape.Tags
		if legacy {
			op.Tags = append(op.Tags, Compat)
		}
		// This loop IS the set zip will build MCP tools from — both read the typed
		// registry and nothing else — so the mark is a restatement of where we are
		// standing rather than a judgement made here. See [Operation.Tool].
		op.Tool = true
		doc.Paths[path][strings.ToLower(method)] = op
	}
	// MERGE, never replace: From may already have named schemas from Register.
	// On a name both clients claim, the typed op wins — its schema is derived from
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

// Spec reads the live router and every projection of it — the one call a caller
// wants.
//
// Three readings, in the order each earns: the router gives the total set of
// addresses, the typed registry gives detail for the ones that have any, and the
// relays give what is BEHIND the addresses that are relays rather than endpoints.
// Project runs last because a relay is only a relay once the router has been read,
// and because what it substitutes is already a finished document.
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
	if err := Project(doc, relays()); err != nil {
		return nil, err
	}
	// Audience is stamped LAST, on the finished set of addresses, because it is
	// the only point at which every operation this app publishes exists at its
	// published address: Fold replaces structural operations with typed ones and
	// Project replaces relays with what is behind them, so a mark written before
	// either would be thrown away by it. See openapi/public.go.
	stamp(doc)
	// And the credential, on that same finished set, for that same reason. Two
	// facts a route table cannot carry, declared beside the routes and written
	// here where every operation exists at its published address.
	secure(doc)
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

// Mount serves the document at Path off app's OWN live router — the app it is
// registered on is the app it reads, so what it serves is that process's actual
// route table and nothing else.
//
// It is UNAUTHENTICATED, deliberately:
//
//   - It is an API description of a public API, and it grants no capability.
//     Every route it names stays individually auth-gated; reading the map admits
//     nothing. Withholding it would be obscurity, not access control.
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
//
// The light HOST mounts no subsystem, so its live router is not the API — see
// [UseFleet], the other document source at this same one address.
func Use(app *zip.App, info Info, servers ...Server) {
	serve(app, func() (*Document, error) { return Spec(app, info, servers...) })
}

// serve registers Path and answers it with whatever doc produces, rendered ONCE
// on the first request.
//
// One registrar for both document sources, so the address, the status, the
// encoding and the failure mode are stated once and cannot drift between them.
//
// LAZY is load-bearing in both: it is what lets Mount's document contain the
// route this very call registers, and what keeps UseFleet's compose off the
// host's boot path.
//
// ONCE covers the bytes, not just the value, and that is not an optimization —
// it is the shape of what this is. The route table is fixed after boot, so the
// document is immutable and re-encoding it per request is work whose answer
// cannot change. It matters because the endpoint is public and unauthenticated on
// the edge router: the fleet document is megabytes, and re-marshalling it
// per request is an amplifier anyone can pull. Rendered, a repeat request is a
// memcpy and the Document itself is collectable.
//
// encoding/json rather than the app's own encoder, because these are the bytes
// openapi.yaml is rendered from (openapi/compose_test.go): the served document and
// the committed artifact are then the same bytes, not two encodings that agree.
// The document endpoint describes ITSELF, in an init rather than in serve, because
// serve runs once per document source (Mount and UseFleet) and Describe refuses a
// duplicate — a self-description that panicked the second time a process mounted
// would be worse than none.
//
// Neither address has an owning subsystem, so nothing else would ever declare
// them, and Path was the last route in the fleet publishing an operationId and
// nothing else. A generated SDK offers it as a method; a spec-derived CLI offers it
// as a command. Both should be able to say what it is.
func init() {
	Describe(Path, http.MethodGet,
		"The API description this SDK was generated from",
		"Serves the OpenAPI document for the routes this process actually answers — "+
			"generated from the live router at request time, not from a checked-in file that "+
			"can disagree with it.\n\n"+
			"On an app it is that app's own surface; on the fleet's public endpoint it is the composed "+
			"document for every mounted app. Unauthenticated by design: a client has to be able "+
			"to read the contract before it holds a credential, and the document grants nothing.\n\n"+
			"Rendered once and served as bytes thereafter, so the route table's immutability is "+
			"what makes a repeat request a memcpy rather than a re-encode of a megabyte document.")
	// And it says so in the contract, not only in the prose above. The reasoning
	// on [Mount] is what makes this endpoint unauthenticated; without the declaration
	// the document would inherit the fleet requirement and tell every generated
	// client to hold a credential before it can read the description it needs in
	// order to know what a credential is for. CommandPath is the same endpoint under
	// [serve] and gets the same answer (command.go).
	Open(Path, http.MethodGet)

	// The alias owes the same two declarations, because the document does not
	// know it is an alias: [WellKnown] is a second address in the published
	// contract, so silence there is an SDK method and a CLI command carrying an
	// operationId and nothing else — the exact hole Path's own prose was written
	// to close. Its summary says WHICH of the two it is, since a reader meets
	// them side by side in a list of operations and the difference is the whole
	// content of this one.
	Describe(WellKnown, http.MethodGet,
		"The API description, at the conventional address",
		"The same document /v1/openapi.json serves, at the address RFC 8615 reserves "+
			"for discovery — one handler over one render, so the two cannot describe "+
			"different APIs.\n\n"+
			"It exists because a client that has never seen this API probes here first. "+
			"Prefer /v1/openapi.json when you already know the API: it is canonical, and "+
			"it is what the document's own self-description names.")
	Open(WellKnown, http.MethodGet)
}

func serve(app *zip.App, doc func() (*Document, error)) {
	render := sync.OnceValues(func() ([]byte, error) {
		d, err := doc()
		if err != nil {
			return nil, err
		}
		return json.Marshal(d)
	})
	answer := func(c *zip.Ctx) error {
		body, err := render()
		if err != nil {
			return zip.ErrInternal(err.Error())
		}
		c.SetHeader("Content-Type", "application/json")
		return c.Bytes(200, body)
	}
	app.Get(Path, answer)
	// Both addresses, one handler over one render, so the canonical path and the
	// conventional one cannot come to describe different APIs. See [WellKnown]
	// for why the alias has to exist at all.
	app.Get(WellKnown, answer)
	// The command projection reads the SAME render, so both addresses are two
	// readings of one artifact. Here rather than in either mount, because both
	// document sources go through serve and the fifth projection belongs to
	// whichever one this deployment has (command.go).
	serveCommands(app, render)
}

// MCP reports whether path is the host's — one [serve] registers (the document
// and its command projection), the agent MCP server (openapi/mcp.go), or the
// index a client follows from the API root (openapi/index.go).
//
// The endpoints are the host's, and they are the only operations that belong to
// NO app: they are declared in this package rather than beside any subsystem, no
// manifest row names them, and EVERY deployment serves them whatever subset of
// the fleet it runs. Four gates need exactly that fact and each had written it
// as the one literal that was true when it was written:
//
//	Complete                     skips a description no app can own
//	misfiled.go                  files them where the protocol puts them
//	cmd/cloud/openapi_test.go    exempts them from a scoped deployment's surface
//	manifest/openapi_test.go     refuses an app row that claims one — [Routed]
//
// So it is stated once, beside the code that makes it true. The second endpoint
// arrived and all three were wrong the same afternoon — the cost of a literal is
// that it is right until it isn't and says nothing when it stops.
//
// Whether the host REGISTERS a route at one of them is a narrower question, and
// the last gate is the one that asks it: see [Routed].
func Host(path string) bool {
	// Either spelling answers, because the callers hold different ones: a prose
	// declaration is keyed by the ROUTER's pattern (/v1/:name) and the document by
	// the rendered template (/v1/{name}). translate is what already maps one onto
	// the other, so this asks it rather than listing both.
	at, _ := translate(path)
	switch at {
	case Path, WellKnown, CommandPath, mcp.Path, RootPath, IndexPath:
		return true
	}
	return false
}

// Routed reports whether the host registers a ROUTE at this endpoint, rather
// than answering it ahead of the router.
//
// Both index endpoints sit inside a subtree a manifest row already claims — /v1
// is ai's REMAINDER and /v1/{name} is each capability's own root — so a route at
// either is two definitions claiming one address and zip refuses the composition
// outright. [UseIndex] answers them from middleware composed before the mounts
// instead, which no Load can sit in front of.
//
// That distinction is the whole content of manifest.TestNoAppClaimsAHostEndpoint: a
// row may not claim an address the host ROUTES, because fiber would merge the two
// patterns and leave the host's handler behind the proxy; it may claim one the
// host answers ahead of the router, because there is nothing there to merge with.
func Routed(path string) bool {
	at, _ := translate(path)
	return Host(at) && at != RootPath && at != IndexPath
}
