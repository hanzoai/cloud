package tracker

// A route can be deleted and nothing notices. That is not hypothetical here:
// GET /v1/tracker/issues and the claim route were registered, verified live in
// production (403, gated), and then lost from tracker.go during a rebase against
// another lane editing the same checkout. The handlers stayed, search.go stayed,
// the store's filter stayed — only the two lines that REACH them went. The
// package compiled, every test passed, and production answered 404.
//
// The 404-vs-403 difference is the whole tell: /v1/tracker/projects answered 403
// (a gate refusing) while /v1/tracker/issues answered 404 (nothing there). That
// is only visible if someone re-probes, and a probe is only true of the commit it
// ran against.
//
// So the surface is written down. Adding a route means adding it here, which is
// the point: a route nobody declared is a route nobody decided on, and a route
// that vanishes fails a test instead of a user.

import "testing"

// trackerSurface is every route this app serves under /v1/tracker.
var trackerSurface = []string{
	"GET /v1/tracker/projects",
	"GET /v1/tracker/projects/:key",
	"GET /v1/tracker/projects/:key/issues",
	"POST /v1/tracker/projects",
	"POST /v1/tracker/projects/:key/issues",
	"PATCH /v1/tracker/projects/:key/issues/:num",
	"DELETE /v1/tracker/projects/:key",
	"GET /v1/tracker/milestones",

	// Find work across every project, and take it. These read the local store
	// rather than the forge, which answers per repository.
	"GET /v1/tracker/issues",
	"POST /v1/tracker/projects/:key/issues/:num/claim",
}

func TestTrackerSurfaceIsRegistered(t *testing.T) {
	app := mountWire(t)

	live := map[string]bool{}
	for _, r := range app.Fiber().GetRoutes(true) {
		if r.Method == "HEAD" { // fiber mirrors every GET; not a surface of ours
			continue
		}
		live[r.Method+" "+r.Path] = true
	}
	for _, want := range trackerSurface {
		if !live[want] {
			t.Errorf("%s is declared but NOT a live route — the registration is gone, "+
				"which production reports as 404 rather than as a refusal", want)
		}
	}
}
