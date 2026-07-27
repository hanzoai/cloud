package cloud_test

// The control for the outage class this repo shipped twice: ONE embedded subsystem
// installs middleware on the shared app and every subsystem mounted after it is
// gated by a stranger. Blast radius used to be a slice position in a 128-entry
// composition root. These tests pin that it is now a declaration.

import (
	"net/http"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// deny is the middleware a subsystem installs when it means to gate its own
// surface: it answers 401 and never continues.
func deny(c *zip.Ctx) error { return c.JSON(http.StatusUnauthorized, map[string]string{"e": "denied"}) }

// pong is a plain route: reaching it means nothing gated the request.
func pong(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]string{"ok": "pong"}) }

// get issues an in-memory request and returns the status.
func get(t *testing.T, app *zip.App, path string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, "http://cloud.test"+path, nil)
	if err != nil {
		t.Fatalf("NewRequest %s: %v", path, err)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s: %v", path, err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func mountAll(t *testing.T, app *zip.App, specs []cloud.AppSpec) error {
	t.Helper()
	enable := make([]string, 0, len(specs))
	for _, s := range specs {
		enable = append(enable, s.Name)
	}
	return cloud.MountAll(app, specs,
		&cloud.Config{Enable: enable},
		cloud.Deps{Logger: luxlog.NewNoOpLogger()})
}

func newApp() *zip.App {
	return zip.New(zip.Config{Logger: luxlog.NewNoOpLogger(), DisableStartupMessage: true})
}

// TestScopeConfinesUseToTheSubsystem is the two-subsystem control. "guard" installs
// app-wide middleware that refuses everything; "neighbour" registers an ordinary
// route. Before scoping, guard's Use landed on the shared app and neighbour — and
// every other subsystem after it in Wire() — answered 401. Now guard gates only its
// own subtree, and it does so without declaring anything: an empty Prefixes means
// the /v1/<name> convention every subsystem already follows.
func TestScopeConfinesUseToTheSubsystem(t *testing.T) {
	app := newApp()
	err := mountAll(t, app, []cloud.AppSpec{
		{Name: "guard", Mount: func(r cloud.Router, _ cloud.Deps) error {
			r.Use(deny)
			r.Get("/v1/guard/whoami", pong)
			return nil
		}},
		{Name: "neighbour", Mount: func(r cloud.Router, _ cloud.Deps) error {
			r.Get("/v1/neighbour/ping", pong)
			return nil
		}},
	})
	if err != nil {
		t.Fatalf("MountAll: %v", err)
	}

	if got := get(t, app, "/v1/neighbour/ping"); got != http.StatusOK {
		t.Errorf("neighbour = %d, want 200 — guard's middleware escaped its subsystem", got)
	}
	if got := get(t, app, "/v1/guard/whoami"); got != http.StatusUnauthorized {
		t.Errorf("guard = %d, want 401 — the subsystem's own gate must still run", got)
	}
}

// TestScopeHonoursDeclaredPrefixes proves Prefixes is the thing that widens the
// gate, and that it widens it to exactly what was declared. IAM is the real case:
// it owns two subtrees and neither of them is /v1/iam alone.
func TestScopeHonoursDeclaredPrefixes(t *testing.T) {
	app := newApp()
	err := mountAll(t, app, []cloud.AppSpec{
		{Name: "identity", Prefixes: []string{"/v1/identity", "/login/oauth"},
			Mount: func(r cloud.Router, _ cloud.Deps) error {
				r.Use(deny)
				r.Get("/v1/identity/me", pong)
				r.Get("/login/oauth/authorize", pong)
				return nil
			}},
		{Name: "neighbour", Mount: func(r cloud.Router, _ cloud.Deps) error {
			r.Get("/v1/neighbour/ping", pong)
			return nil
		}},
	})
	if err != nil {
		t.Fatalf("MountAll: %v", err)
	}

	for _, p := range []string{"/v1/identity/me", "/login/oauth/authorize"} {
		if got := get(t, app, p); got != http.StatusUnauthorized {
			t.Errorf("%s = %d, want 401 — a declared prefix must be gated", p, got)
		}
	}
	if got := get(t, app, "/v1/neighbour/ping"); got != http.StatusOK {
		t.Errorf("neighbour = %d, want 200 — an undeclared subtree must stay open", got)
	}
}

// TestScopeRefusesMiddlewareOutsideItsPrefixes covers the second door: Group carries
// middleware too, and a group at someone else's prefix gates their routes. The mount
// fails, so the binary never boots half-gated — and the middleware is not installed
// even in the failed attempt.
func TestScopeRefusesMiddlewareOutsideItsPrefixes(t *testing.T) {
	app := newApp()
	err := mountAll(t, app, []cloud.AppSpec{
		{Name: "neighbour", Mount: func(r cloud.Router, _ cloud.Deps) error {
			r.Get("/v1/neighbour/ping", pong)
			return nil
		}},
		{Name: "greedy", Mount: func(r cloud.Router, _ cloud.Deps) error {
			r.Group("/v1", deny)
			return nil
		}},
	})
	if err == nil {
		t.Fatal("MountAll succeeded — a subsystem gated /v1 without declaring it")
	}
	if got := get(t, app, "/v1/neighbour/ping"); got != http.StatusOK {
		t.Errorf("neighbour = %d, want 200 — the refused middleware was installed anyway", got)
	}
}

// TestScopeAllowsGroupInsideItsPrefixes keeps the refusal honest: a subsystem
// rate-limiting a leaf of its OWN subtree is the normal case and must pass.
func TestScopeAllowsGroupInsideItsPrefixes(t *testing.T) {
	app := newApp()
	err := mountAll(t, app, []cloud.AppSpec{
		{Name: "vault", Mount: func(r cloud.Router, _ cloud.Deps) error {
			r.Group("/v1/vault/auth", deny)
			r.Get("/v1/vault/auth/login", pong)
			r.Get("/v1/vault/status", pong)
			return nil
		}},
	})
	if err != nil {
		t.Fatalf("MountAll: %v", err)
	}
	if got := get(t, app, "/v1/vault/auth/login"); got != http.StatusUnauthorized {
		t.Errorf("vault auth = %d, want 401", got)
	}
	if got := get(t, app, "/v1/vault/status"); got != http.StatusOK {
		t.Errorf("vault status = %d, want 200 — the group gate must not cover the whole subsystem", got)
	}
}

// TestGlobalIsTheOnlyAppWideDoor pins the asymmetry that makes the whole thing
// work: with Global the subsystem receives the bare *zip.App and its Use means what
// it has always meant. That is the capability, and it is spelled out in Wire().
func TestGlobalIsTheOnlyAppWideDoor(t *testing.T) {
	app := newApp()
	err := mountAll(t, app, []cloud.AppSpec{
		{Name: "edge", Global: true, Mount: cloud.Global(func(a *zip.App, _ cloud.Deps) error {
			a.Use(deny)
			return nil
		})},
		{Name: "neighbour", Mount: func(r cloud.Router, _ cloud.Deps) error {
			r.Get("/v1/neighbour/ping", pong)
			return nil
		}},
	})
	if err != nil {
		t.Fatalf("MountAll: %v", err)
	}
	if got := get(t, app, "/v1/neighbour/ping"); got != http.StatusUnauthorized {
		t.Errorf("neighbour = %d, want 401 — Global middleware must still reach every route", got)
	}
}

// TestGlobalMountNeedsTheGlobalFlag proves the adapter cannot be smuggled in
// without the declaration: a spec that takes the bare app but forgets Global: true
// fails the mount instead of silently receiving a scope it cannot use.
func TestGlobalMountNeedsTheGlobalFlag(t *testing.T) {
	app := newApp()
	err := mountAll(t, app, []cloud.AppSpec{
		{Name: "edge", Mount: cloud.Global(func(*zip.App, cloud.Deps) error { return nil })},
	})
	if err == nil {
		t.Fatal("MountAll succeeded — cloud.Global ran without Global: true")
	}
}
