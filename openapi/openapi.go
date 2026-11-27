package openapi

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/hanzoai/cloud/sdk/mcp"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

// Path is the canonical spec endpoint. House law: /v1/ only, no /api/ prefix,
// and never a v2 — the document's own shape is versioned by its `openapi` field.
const Path = "/v1/openapi.json"

// RootSpec is the unversioned spelling a client tries when it guesses. It
// redirects to [Path]; see [serve].
const RootSpec = "/openapi.json"

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
func Use(app *zip.App, info spec.Info, servers ...spec.Server) {
	serve(app, func() (*spec.Document, error) { return spec.Of(app, info, servers...) })
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
	spec.Describe(Path, http.MethodGet,
		"The API description this SDK was generated from",
		"Serves the OpenAPI document for the routes this process actually answers — "+
			"generated from the live router at request time, not from a checked-in file that "+
			"can disagree with it.\n\n"+
			"On an app it is that app's own surface; on the fleet's public endpoint it is the composed "+
			"document for every mounted app. Unauthenticated by design: a client has to be able "+
			"to read the contract before it holds a credential, and the document grants nothing.\n\n"+
			"Rendered once and served as bytes thereafter, so the route table's immutability is "+
			"what makes a repeat request a memcpy rather than a re-encode of a megabyte document.\n\n"+
			"The whole document is megabytes, so ask for less. ?capability=<name> returns only that "+
			"capability's operations and the schemas they reach (?tag=<name> is the same filter); "+
			"?operation=<operationId> returns one operation; ?path=<prefix> returns the operations "+
			"whose address starts with that prefix. They combine. A slice is a valid document on its own. GET /v1 lists the capabilities and "+
			"GET /v1/<name> lists one capability's operations, so a reader can walk from the index "+
			"down to a single schema without fetching the rest. A query nothing matches is 404.")
	// And it says so in the contract, not only in the prose above. The reasoning
	// on [Mount] is what makes this endpoint unauthenticated; without the declaration
	// the document would inherit the fleet requirement and tell every generated
	// client to hold a credential before it can read the description it needs in
	// order to know what a credential is for. CommandPath is the same endpoint under
	// [serve] and gets the same answer (command.go).
	spec.Open(Path, http.MethodGet)

	// The alias owes the same two declarations, because the document does not
	// know it is an alias: [WellKnown] is a second address in the published
	// contract, so silence there is an SDK method and a CLI command carrying an
	// operationId and nothing else — the exact hole Path's own prose was written
	// to close. Its summary says WHICH of the two it is, since a reader meets
	// them side by side in a list of operations and the difference is the whole
	// content of this one.
	spec.Describe(WellKnown, http.MethodGet,
		"The API description, at the conventional address",
		"The same document /v1/openapi.json serves, at the address RFC 8615 reserves "+
			"for discovery — one handler over one render, so the two cannot describe "+
			"different APIs.\n\n"+
			"It exists because a client that has never seen this API probes here first. "+
			"Prefer /v1/openapi.json when you already know the API: it is canonical, and "+
			"it is what the document's own self-description names.")
	spec.Open(WellKnown, http.MethodGet)
}

// documentCache is what a cache may keep of the document and of a slice of it. It
// is rendered once a process and is the same for every caller, so it is public: a
// browser holds it a minute, a shared cache five, and either may serve it stale for
// a day while it fetches the next (the terms of cloud's publicCatalog). It was
// answered with no Cache-Control, so every read of its five megabytes reached this
// process.
const documentCache = "public, max-age=60, s-maxage=300, stale-while-revalidate=86400"

func serve(app *zip.App, doc func() (*spec.Document, error)) {
	render := sync.OnceValues(func() ([]byte, error) {
		d, err := doc()
		if err != nil {
			return nil, err
		}
		return json.Marshal(d)
	})
	narrow := sync.OnceValues(func() (*slicer, error) {
		whole, err := render()
		if err != nil {
			return nil, err
		}
		return newSlicer(whole)
	})
	answer := func(c *zip.Ctx) error {
		capability, operation, under := c.Query("capability"), c.Query("operation"), c.Query("path")
		// ?tag= is the OpenAPI reader's word for a capability (slice.go). Two
		// different names in one query ask for nothing, so that is a refusal
		// rather than a choice between them.
		if tag := c.Query("tag"); tag != "" {
			if capability != "" && capability != tag {
				return zip.ErrBadRequest("tag and capability name the same thing; give one, or the same name twice")
			}
			capability = tag
		}
		if capability == "" && operation == "" && under == "" {
			body, err := render()
			if err != nil {
				return zip.ErrInternal(err.Error())
			}
			c.SetHeader("Content-Type", "application/json")
			c.SetHeader("Cache-Control", documentCache)
			return c.Bytes(200, body)
		}
		if (capability != "" && !name.MatchString(capability)) || (operation != "" && !name.MatchString(operation)) {
			return zip.ErrBadRequest("capability and operation are names: letters, digits, '.', '_' and '-'")
		}
		if under != "" && !prefix.MatchString(under) {
			return zip.ErrBadRequest("path is an address prefix: '/' then letters, digits, '.', '_', '~', '-', '/', '{' and '}'")
		}
		s, err := narrow()
		if err != nil {
			return zip.ErrInternal(err.Error())
		}
		body, ok, err := s.slice(capability, operation, under)
		if err != nil {
			return zip.ErrInternal(err.Error())
		}
		if !ok {
			return zip.ErrNotFound("no operation matches; GET /v1 lists the capabilities and GET /v1/<capability> its operations")
		}
		c.SetHeader("Content-Type", "application/json")
		c.SetHeader("Cache-Control", documentCache)
		return c.Bytes(200, body)
	}
	app.Raw(http.MethodGet, Path, answer)
	// Both addresses, one handler over one render, so the canonical path and the
	// conventional one cannot come to describe different APIs. See [WellKnown]
	// for why the alias has to exist at all.
	app.Raw(http.MethodGet, WellKnown, answer)
	// /openapi.json at the root is the third place a client guesses, and it was a
	// 404 (hanzo-inc/cloud#431). It redirects rather than answers, query and all,
	// so there is still one canonical address; and it is undeclared, because a
	// redirect is not an operation a caller can DO — published, it would be an SDK
	// method and a CLI verb that fetch the document a second way.
	app.Undeclared().Raw(http.MethodGet, RootSpec, func(c *zip.Ctx) error {
		to := Path
		if q := c.Fiber().Request().URI().QueryString(); len(q) > 0 {
			to += "?" + string(q)
		}
		return c.Redirect(http.StatusMovedPermanently, to)
	})
	// The command projection reads the SAME render, so both addresses are two
	// readings of one artifact. Here rather than in either mount, because both
	// document sources go through serve and the fifth projection belongs to
	// whichever one this deployment has (command.go).
	serveCommands(app, render)
}

// Host reports whether path is the host's — one [serve] registers (the document
// and its command projection), the agent MCP server (openapi/mcp.go), the index
// a client follows from the API root (openapi/index.go), or a document of the
// agent's door (openapi/agent.go).
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
func Host(path string) bool { return spec.Reserved(path) }

// The host states its own addresses to the declaring side once, here, beside
// the code that serves them: [spec.Complete] judges an app's prose against them.
func init() {
	spec.Reserve(Path, WellKnown, CommandPath, mcp.Path, spec.RootPath, IndexPath)
	for _, p := range pages {
		spec.Reserve(p.path)
	}
}

// Routed reports whether the host registers a ROUTE at this endpoint, or answers
// it BEHIND the mounts (the agent's door), rather than answering it ahead of the
// router. Either way an app row claiming the address would take it first.
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
	at, _ := spec.Translate(path)
	return Host(at) && at != spec.RootPath && at != IndexPath
}
