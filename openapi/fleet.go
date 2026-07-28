package openapi

import "github.com/zap-proto/zip"

// The IDENTITY of the Hanzo Cloud API document, in one place.
//
// There are now four projections of this one API, and they are compared against
// each other by TEST:
//
//	the woven golden               openapi.yaml, written by the weave (make openapi)
//	each app binary's own subset   `<app> openapi`, one file per app
//	the woven fleet document       Weave() over those subsets
//	the live endpoint              GET /v1/openapi.json
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
var (
	fleetInfo = Info{
		Title:   "Hanzo Cloud API",
		Version: "v1",
		Description: "Generated from the live router — every operation below is a route the " +
			"unified cloud binary actually serves. Tagged by product: the first path segment after /v1/.",
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
