package cloud_test

// THE GATE THAT WAS MISSING. Nothing in this repo called zip's [zip.App.Build]
// from a test, so "this program does not compose" was a fact only a production
// startup could discover — and for a subsystem whose surface no test drove, not
// even then. Three subsystems shipped a middleware seam that could never run:
// auditlog and catalog declared middleware on a group whose subtree held no
// routes (their ops are declared on the App with the whole path, so the routes
// are siblings of that group, not children), and zen installed a Claim on "/v1"
// it had no grant for. The first two turned every test in their own packages
// into a panic out of app.Test; the third failed MountAll outright.
//
// Build is the whole check. zip refuses a program whose middleware wraps nothing
// (walk.go's inert-middleware rule), whose addresses collide, or whose MCP tools
// are claimed twice — and it refuses it BEFORE anything listens. Calling it here
// moves every one of those from a startup panic to a red test.
//
// BOTH ROUTERS, because a subsystem is mounted through two and they are not the
// same. Production goes through MountAll, which hands each Mount a *scope* bound
// to its declared prefixes; a scope rewrites a middleware-carrying Group into an
// app-wide, path-gated Use, so it can HIDE a seam that a bare *zip.App refuses.
// A package's own tests usually mount on the bare app. A subsystem is correct
// only when it composes both ways, so both are asserted.

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/auditlog"
	"github.com/hanzoai/cloud/apps/catalog"
	"github.com/hanzoai/cloud/apps/zen"
	"github.com/hanzoai/cloud/manifest"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// composed is one subsystem as its plugin/<name>/main.go declares it: the mount
// and the MIDDLEWARE GRANT. prefixes is cloud.Plugin.Prefixes — the subtrees
// whose middleware this subsystem may install — which is a different fact from
// manifest.App.Prefixes, the paths the host ROUTES to it. zen is the case that
// proves they are different: it routes nothing and gates "/v1".
var composed = []struct {
	name     string
	mount    cloud.MountFunc
	prefixes []string
}{
	{"audit", auditlog.Mount, nil},
	{"catalog", catalog.Mount, nil},
	{"zen", zen.Mount, []string{"/v1"}},
}

// TestSubsystemComposesThroughMountAll is the PRODUCTION path: MountAll hands
// the Mount a scope bound to its grant. A subsystem that installs middleware
// outside that grant fails here, at the mount, exactly as the binary would —
// which is what zen did, silently, for as long as its grant read the routing
// table instead of its own spec.
func TestSubsystemComposesThroughMountAll(t *testing.T) {
	for _, c := range composed {
		t.Run(c.name, func(t *testing.T) {
			app := newApp()
			err := cloud.MountAll(app,
				[]cloud.Plugin{{Name: c.name, Mount: c.mount, Prefixes: c.prefixes}},
				&cloud.Config{Enable: []string{c.name}},
				cloud.Deps{Logger: luxlog.NewNoOpLogger(), DataDir: t.TempDir()})
			if err != nil {
				t.Fatalf("MountAll(%s): %v", c.name, err)
			}
			if err := app.Build(); err != nil {
				t.Fatalf("Build after mounting %s: %v", c.name, err)
			}
		})
	}
}

// TestSubsystemComposesOnABareApp is the path a package's OWN tests take, and
// the one a scope cannot paper over: a bare *zip.App applies zip's rules to the
// subsystem's program exactly as written. auditlog and catalog were both red
// here — every test in those packages panicked out of app.Test, because Test
// calls prepare, which builds.
func TestSubsystemComposesOnABareApp(t *testing.T) {
	for _, c := range composed {
		t.Run(c.name, func(t *testing.T) {
			app := newApp()
			if err := c.mount(app, cloud.Deps{Logger: luxlog.NewNoOpLogger(), DataDir: t.TempDir()}); err != nil {
				t.Fatalf("Mount(%s): %v", c.name, err)
			}
			if err := app.Build(); err != nil {
				t.Fatalf("Build after mounting %s on a bare app: %v", c.name, err)
			}
		})
	}
}

// TestMiddlewareOverAnEmptySubtreeIsRefused pins the RULE the three fixes obey,
// against the framework rather than against our memory of it. Without this, a
// future zip that stopped refusing the shape would let the seam rot back in and
// every test above would still be green — the subsystems would compose, and the
// middleware would once again never run.
//
// The shape is the exact one that shipped: a group carrying middleware at a
// prefix, and the route registered at that same path on the APP, so it is the
// group's SIBLING and not its child.
func TestMiddlewareOverAnEmptySubtreeIsRefused(t *testing.T) {
	app := newApp()
	app.Group("/v1/thing", func(c *zip.Ctx) error { return c.Continue() })
	app.Get("/v1/thing", pong)

	if err := app.Build(); err == nil {
		t.Fatal("Build accepted middleware over an empty subtree — the seam this " +
			"file exists to catch would run nowhere and nothing would say so")
	}
}

// TestUseIsTheVerbThatComposesBothWays is the positive control for the fix the
// three subsystems took: Use says "wrap what this app serves" and needs no
// second copy of the subtree, so it is correct on a bare app (root middleware is
// live by construction) and correct through a scope (which gates it to the
// grant). The route keeps its exact path — the reason a group with an empty leaf
// was not the answer, since joining "/v1/thing" with "" yields "/v1/thing/" and
// that is what would ship in OpenAPI and the SDK.
func TestUseIsTheVerbThatComposesBothWays(t *testing.T) {
	app := newApp()
	ran := false
	app.Use(zip.H(func(c *zip.Ctx) error { ran = true; return c.Continue() }))
	app.Get("/v1/thing", pong)

	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := get(t, app, "/v1/thing"); got != 200 {
		t.Fatalf("GET /v1/thing = %d, want 200", got)
	}
	if !ran {
		t.Error("the middleware did not run — composing is not the same as running, " +
			"and this test asserts the second")
	}
	for _, r := range app.Declaration().Routes {
		if r.Pattern == "/v1/thing/" {
			t.Errorf("route pattern drifted to %q — the published path must stay /v1/thing", r.Pattern)
		}
	}
}

// TestZenClaimGatesTheCoresidentHost is the test that would have caught the
// thing none of the others could: zen COMPOSING is not zen RUNNING.
//
// zen answers no path. It works by wrapping ai's "/v1", routing zen-SKU requests
// and Next()ing the rest — so it is only ever correct INSIDE a binary that also
// serves that prefix. It was in none: the light host skips Coresident apps by
// design (a middleware cannot be a separate process), and plugin/ai linked only
// ai. cmd/cloud had already written down the symptom — "zen's child never saw a
// request" — and the mount was refused on top of that, so nothing downstream ever
// got far enough to notice the gate was absent.
//
// This asserts the two halves of the contract that make one prefix serve both:
// a zen SKU is CLAIMED (it does not reach the host's catch-all), and anything
// else FALLS THROUGH untouched. Order is the contract — a Claim installed after
// ai's greedy All("/v1/*") would sit behind the route it exists to gate — so the
// stand-in catch-all here is registered after zen mounts, exactly as ai's is.
func TestZenClaimGatesTheCoresidentHost(t *testing.T) {
	app := newApp()
	if err := cloud.MountAll(app,
		[]cloud.Plugin{{Name: "zen", Mount: zen.Mount, Prefixes: manifest.GrantFor("zen")}},
		&cloud.Config{Enable: []string{"zen"}},
		cloud.Deps{Logger: luxlog.NewNoOpLogger(), DataDir: t.TempDir()}); err != nil {
		t.Fatalf("MountAll(zen): %v", err)
	}
	// ai's position: the greedy catch-all, registered after zen as in plugin/ai.
	reachedHost := false
	app.All("/v1/*", func(c *zip.Ctx) error {
		reachedHost = true
		return c.JSON(200, map[string]string{"served": "host"})
	})
	if err := app.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}

	post := func(model string) int {
		reachedHost = false
		body := strings.NewReader(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"}]}`)
		req := httptest.NewRequest("POST", "/v1/chat/completions", body)
		req.Header.Set("Content-Type", "application/json")
		res, err := app.Test(req)
		if err != nil {
			t.Fatalf("POST %s: %v", model, err)
		}
		return res.StatusCode
	}

	// A zen SKU must be CLAIMED. With no resolvable wallet zen refuses (402) —
	// what matters is that zen answered at all, which it cannot do if the Claim
	// never ran.
	code := post("zen5")
	t.Logf("zen SKU  -> %d, reached host catch-all = %v", code, reachedHost)
	if reachedHost {
		t.Error("a zen SKU reached the host's catch-all — the Claim did not run, so the " +
			"request served unmetered and ungated")
	}

	// Everything else must fall through untouched: zen gates its own family, it
	// does not stand in front of the whole model API.
	code = post("gpt-4o")
	t.Logf("non-zen  -> %d, reached host catch-all = %v", code, reachedHost)
	if !reachedHost {
		t.Errorf("a non-zen model did NOT reach the host (got %d) — the Claim swallowed "+
			"traffic it does not own", code)
	}
}
