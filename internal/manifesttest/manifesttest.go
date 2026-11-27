package manifesttest

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
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
	Name string

	// Grant is the app's own cloud.Plugin.Prefixes, so the mount here bounds the
	// app exactly as its binary does. Empty takes the /v1/<name> convention.
	Grant  []string
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
	t.Setenv(env.DataDirKey, t.TempDir())

	// Mounted through the composition root, so the grant the app receives here is
	// the one a deployed process hands it.
	app := zip.New(zip.Config{Logger: luxlog.New("manifest-gate"), DisableStartupMessage: true})
	spec := cloud.Plugin{Name: c.Name, Price: cloud.Free, Use: c.Use, Prefixes: c.Grant}
	if err := cloud.UseAll(app, []cloud.Plugin{spec}, &cloud.Config{Enable: []string{c.Name}}, cloud.Deps{}); err != nil {
		t.Fatalf("%s: mount:  %v", c.Name, err)
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
