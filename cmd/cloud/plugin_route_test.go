package main

import (
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/apps"
)

// Every prefix a plugin declares must actually reach the child process.
//
// This is the guard the o11y extraction shipped without, and it is why /v1/sentry
// silently stopped reaching o11y for a whole branch: a plugin declared with SOME
// of its prefixes compiles, mounts, boots and passes every route-table test — the
// subtrees nobody named just quietly go elsewhere. Measured on that branch,
// /v1/sentry/health did not even 404: it fell through to the /v1/* AI catch-all
// and answered 503, which is why this asserts on the ROUTE TABLE and not on a
// status code. A response can be plausible and still come from the wrong place.
//
// zip.Load registers exactly two routes per prefix, `<prefix>` and `<prefix>/*`
// (App.mountVia), so their presence IS the host's promise to forward that subtree.
// Add a prefix to a PluginSpec and this test covers it with no edit here.
func TestEveryPluginPrefixRoutesToItsChild(t *testing.T) {
	if testing.Short() {
		t.Skip("mounts every subsystem in apps.Wire(); slow by construction")
	}
	app := fullyMountedApp(t)

	var prefixes []string
	for _, s := range apps.Wire() {
		if s.Plugin {
			prefixes = append(prefixes, s.Prefixes...)
		}
	}
	if len(prefixes) == 0 {
		t.Fatal("no plugin subsystems — this guard proves nothing")
	}

	// The host's own route table: what it will forward, independent of what any
	// handler chooses to answer.
	registered := map[string]bool{}
	for _, r := range app.Fiber().GetRoutes(true) {
		registered[r.Path] = true
	}

	for _, prefix := range prefixes {
		t.Run(prefix, func(t *testing.T) {
			for _, want := range []string{prefix, prefix + "/*"} {
				if !registered[want] {
					t.Fatalf("no host route %q — this subtree is NOT forwarded to the plugin. "+
						"Name every prefix the subsystem owns in its cloud.PluginSpec (it is variadic).", want)
				}
			}
			// And it answers over the socket, not merely in the table: a route that
			// resolves to a dead child is a 502/503 from mountVia, never a 200.
			res, err := app.Fiber().Test(httptest.NewRequest("GET", prefix+"/health", nil))
			if err != nil {
				t.Fatalf("%s: %v", prefix, err)
			}
			defer res.Body.Close()
			if res.StatusCode == 502 || res.StatusCode == 503 {
				t.Fatalf("%s/health -> %d: the route exists but the child did not answer it", prefix, res.StatusCode)
			}
			t.Logf("%s and %s/* forwarded; GET %s/health -> %d from the child", prefix, prefix, prefix, res.StatusCode)
		})
	}
}
