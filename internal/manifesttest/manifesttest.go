// Package manifesttest is the shared gate for one production bug class: an app
// SERVES a path the fleet does not ROUTE to it.
//
// The fleet is one process per app behind a prefix router (manifest.Apps). A
// request reaches an app only if some prefix in that app's row owns the path, so
// a route an app registers and its row does not claim is served by nobody —
// worse than a 404, because the request still lands somewhere: on whichever app
// owns "/" (the console), which answers its HTML shell with a 200. The product
// looks up and is not there.
//
// WHY A SECOND GATE. manifest/router_test.go already asks the real router where
// a published path lands, and it is the right check for everything it can see —
// but it reads each app's plugin/<app>/openapi.json, so it sees only TYPED,
// documented routes. An embedded SPA is served by an untyped All() route. It
// appears in no document, so that gate had nothing to compare and a whole
// product surface went unrouted with every test green.
//
// So this one does not read a document at all. It reads the app's LIVE ROUTER
// DECLARATION (zip's App.Routes, which is "complete by construction — a route
// the plugin serves and does not publish is still declared"), which is the same
// source the process itself answers from. Typed or untyped, published or not: if
// the app will answer it, this sees it.
//
// It lives in the APP's package rather than in manifest's, because only the app
// can Mount itself and manifest must not import the fleet it routes. The
// assertion lives here, once, so every app shares ONE correctness check.
package manifesttest

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/internal/datadir"
	"github.com/hanzoai/cloud/manifest"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Case is one app's routed-surface gate.
//
//   - Name  is the app's manifest.Apps row name — the same string plugin/<app>
//     passes to cloud.Listen, so the prefixes checked are the ones the fleet
//     actually routes with.
//   - Mount is the app's real Mount, called with real Deps, so the routes
//     examined are the ones a deployed process registers.
//   - Exempt, if set, reports patterns this app deliberately serves off its own
//     prefixes. It exists for routes the HOST owns and every app inherits, not
//     as a place to park a defect: a pattern named here is unroutable in the
//     fleet, so anything product-facing belongs in the manifest row instead.
type Case struct {
	Name   string
	Use    func(app cloud.Router, deps cloud.Deps) error
	Exempt func(pattern string) bool
}

// Run mounts the app and asserts every pattern its router will answer is owned
// by a prefix in its manifest row.
func (c Case) Run(t *testing.T) {
	t.Helper()

	prefixes := manifest.PrefixesFor(c.Name)
	if len(prefixes) == 0 {
		t.Fatalf("%s: no manifest.Apps row — the fleet routes nothing to this app", c.Name)
	}

	// The data root is a deployment FACT, resolved from the environment rather than
	// handed over, so a test states it the way a deployment does.
	t.Setenv(datadir.EnvVar, t.TempDir())

	app := zip.New(zip.Config{Logger: luxlog.New("manifest-gate"), DisableStartupMessage: true})
	if err := c.Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("%s: Use:  %v", c.Name, err)
	}

	seen := 0
	unrouted := map[string]bool{}
	for _, r := range app.Routes() {
		p := r.Pattern
		if p == "" || !strings.HasPrefix(p, "/") {
			continue
		}
		if c.Exempt != nil && c.Exempt(p) {
			continue
		}
		seen++
		if !owned(prefixes, p) {
			unrouted[p] = true
		}
	}

	// A gate that examined nothing passes. Every way this inspects zero routes —
	// a Mount that registered none, a declaration that projected none — is a
	// defect elsewhere that would otherwise arrive here as a green tick.
	if seen == 0 {
		t.Fatalf("%s: no routes were examined — this gate proved nothing", c.Name)
	}

	for p := range unrouted {
		t.Errorf("UNROUTED: %s serves %q and no manifest prefix owns it — in the fleet this "+
			"request reaches whoever owns \"/\" (the console) and gets an HTML shell with a 200. "+
			"Add the prefix to this app's manifest.Apps row, or stop serving the route.", c.Name, p)
	}
	t.Logf("%s: %d routes, all owned by %v", c.Name, seen, prefixes)
}

// owned reports whether any prefix owns pattern. A prefix owns its whole
// SUBTREE — the host mounts each plugin with All(prefix) and All(prefix+"/*") —
// so "/v1/todo" owns "/v1/todo" and everything under "/v1/todo/", and
// nothing else. The boundary matters: "/track" must not be read as owning
// "/todo".
func owned(prefixes []string, pattern string) bool {
	for _, p := range prefixes {
		if pattern == p || strings.HasPrefix(pattern, strings.TrimSuffix(p, "/")+"/") {
			return true
		}
	}
	return false
}
