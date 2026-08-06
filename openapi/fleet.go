package openapi

import (
	"encoding/json"
	"fmt"

	"github.com/zap-proto/zip"
)

// The IDENTITY of the Hanzo Cloud API document, in one place.
//
// There are now four projections of this one API, and they are compared against
// each other by TEST:
//
//	the woven golden               openapi.yaml, written by the weave (make describe)
//	each app binary's own subset   `<app> openapi`, one file per app
//	the woven fleet document       Weave() over those subsets
//	the live endpoint              GET /v1/openapi.json
//
// And one projection OF the golden, downstream and in another repo:
// hanzoai/openapi's hanzo.yaml, which every published SDK is generated from.
// It refutes itself against the LIVE endpoint above (`publish.py --served`),
// because that is the only one of the four a repo without a checkout can read.
//
// An info block that differed between them would make two documents OF THE SAME
// API compare unequal for a reason that has nothing to do with the API — which
// would break the composition proof (weave_test.go) over a title string. So the
// identity is a value, defined once and read by all four, not a literal copied
// into each caller.
//
// Version is the API CONTRACT version, never the build's. cloud.Version is
// stamped per release and putting it here would make every build differ from the
// committed golden. House law is /v1 forever, so this is "v1" forever; the
// document's own shape is versioned by its `openapi` field.
//
// THE DESCRIPTION SAYS WHAT PRODUCED THE DOCUMENT, and it is not what it used to
// say. It read "Generated from the live router — every operation below is a route
// the unified cloud binary actually serves." No producer can make that claim.
//
// There is no unified cloud binary; there is a light host that mounts no
// subsystem and 116 app binaries that each project their OWN router when they are
// BUILT. What the host serves is the weave of those projections ([MountFleet]),
// so nothing in production reads a live router, and the artifact is only as fresh
// as the last `make -f mk/fleet.mk subsets`. It shipped stale — one binary
// answered /v1/billing/gpu/eligibility while publishing /v1/billing/gpu-eligibility,
// because the rename commit did not regenerate the subset.
//
// A false provenance is worse than a missing one, because it is READ. hanzoai/cli's
// genspec quoted this exact sentence as the correctness argument for dropping
// operations from its capture: "a route that is not mounted cannot appear in it."
// Every projection downstream — eight SDKs, the MCP tool list, the CLI, the docs —
// inherits whatever this claims.
//
// So it claims exactly what is true and no more: each operation is a route the
// subsystem that publishes it registered in its own router. What it does NOT
// prove, and what only a probe of the deployed host can: that the front door
// delivers that path to that subsystem. It did not, for all 23 of pricing's, until
// the ingress carve that gave them to an edge worker was deleted.
var (
	fleetInfo = Info{
		Title:   "Hanzo Cloud API",
		Version: "v1",
		Description: "Composed from each subsystem's own projection of its router, in the fleet's " +
			"mount order — every operation below is a route the subsystem that publishes it " +
			"registered. Tagged by product: the first path segment after /v1/.",
	}
	fleetServer = Server{URL: "https://api.hanzo.ai"}
)

// FleetSpec projects app into THE Hanzo Cloud API document: Spec, carrying the
// one identity above.
//
// Every producer of a published spec calls this — the monolith's golden, each
// app binary's subset, and the weave's output. Spec stays exported for the
// callers that want a document of their own (the live endpoint names the
// deployment's brand), but nothing that writes an artifact should be choosing
// its own title.
func FleetSpec(app *zip.App) (*Document, error) {
	return Spec(app, fleetInfo, fleetServer)
}

// Subsets decodes the apps' own documents, in the order given — which callers
// take from manifest.Names(), the fleet's mount order, so a conflict is reported
// as the router would meet it.
//
// read answers with one app's subset bytes. WHERE those bytes come from is the
// caller's, because there are two callers and ONE set of files: the drift gate
// reads the working tree it is about to compare against (openapi/weave_test.go),
// and the light host reads what it embedded from that same tree at build time
// (plugin.Spec). Neither is a second source — check regenerates the files
// both read, from source, and fails on any diff.
//
// A missing subset is refused rather than skipped. Skipping it would publish a
// fleet document with one app's whole surface quietly absent, which is precisely
// the failure mode plugin/ingress cost eight paths to.
//
// AND EACH SUBSET IS CHECKED FOR THE INVARIANT ITS GENERATOR ALREADY OWES, at the
// one place the app's NAME is still in hand. [From] refuses to emit a document
// whose operationIds collide, so a generated subset cannot arrive broken — but
// "generated" was an assumption about a committed file, and a file can be edited.
// One was: /v1/billing/methods was hand-written into commerce's subset by copying
// the /v1/billing/portal/methods block, operationId and prose together, so two
// paths claimed get_v1_billing_portal_methods and the fleet could not be woven at
// all. [Weave] did catch it — but a collision INSIDE one part reaches Weave as a
// collision between two paths with no app attached, so the report named the two
// addresses and left which of 123 subsets to a search. Here the answer is the
// loop variable.
//
// It is the same check, not a second one: uniqueOperationIDs is the single
// statement of the rule, asked once per part here and once over the whole
// composition there, because a part being injective and the weave being injective
// are different facts and neither implies the other.
func Subsets(apps []string, read func(app string) []byte) ([]Part, error) {
	out := make([]Part, 0, len(apps))
	for _, name := range apps {
		raw := read(name)
		if len(raw) == 0 {
			return nil, fmt.Errorf("%s publishes no subset — every app describes itself; run `make -f mk/fleet.mk subsets`", name)
		}
		var doc Document
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, fmt.Errorf("%s subset: %w", name, err)
		}
		if err := uniqueOperationIDs(&doc); err != nil {
			return nil, fmt.Errorf("%s subset: %w — its own generator refuses this, so the file was "+
				"not generated; run `make -C apps/%s describe` rather than editing it", name, err, name)
		}
		out = append(out, Part{App: name, Doc: &doc})
	}
	return out, nil
}

// core is the one operation no app owns: the endpoint that serves the document.
// It is MOUNTED and projected rather than written down — a hand-kept literal
// would be a second definition of a route zip already knows, free to disagree
// with the address the fleet actually answers on.
func core() (Part, error) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	Mount(app, Info{})
	doc, err := FleetSpec(app)
	if err != nil {
		return Part{}, fmt.Errorf("core: %w", err)
	}
	return Part{App: "openapi", Doc: doc}, nil
}

// Fleet is THE published Hanzo Cloud API document: the weave of every app's own
// subset plus core.
//
// ONE definition, called by both things that must agree about it — the gate that
// WRITES openapi.yaml (openapi/weave_test.go) and the host that SERVES it
// (MountFleet). That is what makes "the served document is the committed
// artifact" true by construction rather than by two pieces of code happening to
// agree; a drift between them is not expressible.
func Fleet(subsets []Part) (*Document, error) {
	c, err := core()
	if err != nil {
		return nil, err
	}
	parts := make([]Part, 0, len(subsets)+1)
	parts = append(parts, subsets...)
	parts = append(parts, c)
	return Weave(parts)
}

// MountFleet serves the FLEET's document at Path: the composition of what this
// deployment's plugins serve, woven from the subsets their binaries projected
// when they were built.
//
// It exists because [Mount]'s answer is WRONG on the light host, and wrong in the
// way that is hardest to see. The host mounts no subsystem — that laziness is
// what makes 113 of them affordable — so its live router is 113 proxy prefixes
// and a console catch-all, and reading it describes the ROUTER, not the API. Nor
// can the host mount them to find out: waking the fleet to answer a public GET
// is exactly the cost lazy mounting exists to avoid.
//
// So the host answers from the build-time projection instead. It is the same
// question every other projection answers ("what does the fleet serve") sourced
// from the only place the host can honestly read it. The weave runs ONCE, on the
// first request, off bytes already in the binary: no subsystem starts, no socket
// opens, and a deployment that never gets asked never pays.
//
// Unauthenticated for the reasons stated on [Mount]: same document, same door.
func MountFleet(app *zip.App, subsets func() ([]Part, error)) {
	serve(app, func() (*Document, error) {
		parts, err := subsets()
		if err != nil {
			return nil, err
		}
		return Fleet(parts)
	})
}
