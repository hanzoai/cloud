package cloud_test

// The idiom that took ten plugins down had no test.
//
//	g := r.Group("/v1/x"); g.Use(mw)
//
// scope_test.go covers Use at the subsystem root, Use widened by declared
// prefixes, and Group(prefix, mw) — the form that carries its middleware as an
// argument. It never covered the form that CRASHED: take the group, then call
// Use on it. That one used to hand back the raw zip router, so the middleware
// landed on a node whose subtree was empty (typed ops register through ZipApp,
// on the root) and zip refused to compose the program:
//
//	panic: the group "/v1/account" declares middleware at scope.go
//	       and no routes anywhere beneath it
//
// A test that only asserts the panic is gone would pass on a scope that quietly
// dropped the middleware — which is the worse failure, because the panic is
// honest and a dead guard is not. So each case here asserts BOTH halves: the
// guarded path answers 401 and its unguarded sibling answers 200.

import (
	"net/http"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// TestGroupThenUseGuardsOnlyItsSubtree runs the crash idiom through three
// subsystems at once, so the assertion is not one lucky path:
//
//   - account gates a leaf of its own subtree and leaves the rest open — the
//     shape whose panic named /v1/account;
//   - avatar declares a prefix that is not /v1/avatar, gates a leaf of THAT,
//     and proves a declared prefix and the crash idiom compose together;
//   - vault nests a group inside a group, which is where a bound that behaved
//     like a place would leak into the parent.
//
// Every one of them must compose (MountAll succeeds — zip's inert-middleware
// rule runs on Test's Build), guard exactly its own subtree, and leave every
// sibling reachable.
func TestGroupThenUseGuardsOnlyItsSubtree(t *testing.T) {
	app := newApp()
	err := mountAll(t, app, []cloud.Plugin{
		{Name: "account", Mount: func(r cloud.Router, _ cloud.Deps) error {
			g := r.Group("/v1/account/admin")
			g.Use(zip.H(deny))
			r.Get("/v1/account/admin/purge", pong)
			r.Get("/v1/account/me", pong)
			return nil
		}},
		{Name: "avatar", Prefixes: []string{"/v1/avatar", "/v1/gravatar"},
			Mount: func(r cloud.Router, _ cloud.Deps) error {
				g := r.Group("/v1/gravatar/admin")
				g.Use(zip.H(deny))
				r.Get("/v1/gravatar/admin/flush", pong)
				r.Get("/v1/gravatar/image", pong)
				r.Get("/v1/avatar/image", pong)
				return nil
			}},
		{Name: "vault", Mount: func(r cloud.Router, _ cloud.Deps) error {
			auth := r.Group("/v1/vault/auth")
			admin := auth.Group("/admin")
			admin.Use(zip.H(deny))
			r.Get("/v1/vault/auth/admin/rotate", pong)
			r.Get("/v1/vault/auth/login", pong)
			r.Get("/v1/vault/status", pong)
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

	for _, c := range []struct {
		path string
		want int
	}{
		// Guarded: the middleware the group carries must actually run.
		{"/v1/account/admin/purge", http.StatusUnauthorized},
		{"/v1/gravatar/admin/flush", http.StatusUnauthorized},
		{"/v1/vault/auth/admin/rotate", http.StatusUnauthorized},

		// Unguarded siblings: same subsystem, outside the group's bound.
		{"/v1/account/me", http.StatusOK},
		{"/v1/gravatar/image", http.StatusOK},
		{"/v1/avatar/image", http.StatusOK},
		{"/v1/vault/auth/login", http.StatusOK},
		{"/v1/vault/status", http.StatusOK},

		// A stranger's subtree: a group bound is not app-wide.
		{"/v1/neighbour/ping", http.StatusOK},
	} {
		if got := get(t, app, c.path); got != c.want {
			t.Errorf("%s = %d, want %d", c.path, got, c.want)
		}
	}
}

// TestGroupPrefixesTheRoutesRegisteredThroughIt is the address half, and it is
// the one no compose check can catch: a group that forgot to prepend its prefix
// registers /x instead of /v1/guide/x, and the program still composes. It is
// only ever found by asking for the address.
//
// guide installs no middleware, deliberately. Gating and addressing are two
// claims and a gated route answers 401 whether or not it exists — so a test that
// asserted both at once could not tell "the route is where I said" from "the
// guard refused a 404". They are asked separately: here, and above.
func TestGroupPrefixesTheRoutesRegisteredThroughIt(t *testing.T) {
	app := newApp()
	err := mountAll(t, app, []cloud.Plugin{
		{Name: "guide", Mount: func(r cloud.Router, _ cloud.Deps) error {
			g := r.Group("/v1/guide")
			g.Get("/topics", pong)         // -> /v1/guide/topics
			g.Group("/v2").Get("/x", pong) // -> /v1/guide/v2/x
			return nil
		}},
	})
	if err != nil {
		t.Fatalf("MountAll: %v", err)
	}
	for _, p := range []string{"/v1/guide/topics", "/v1/guide/v2/x"} {
		if got := get(t, app, p); got != http.StatusOK {
			t.Errorf("%s = %d, want 200 — a group route did not land at its prefixed address", p, got)
		}
	}
	for _, p := range []string{"/topics", "/x", "/v2/x"} {
		if got := get(t, app, p); got != http.StatusNotFound {
			t.Errorf("%s = %d, want 404 — a group route was registered without its prefix", p, got)
		}
	}
}

// TestScopedUseGatesOnlyWhatFollowsIt pins the ONE ordering rule a subsystem
// author has to know, because it is the difference between a guard and a
// decoration and nothing announces it.
//
// A scope installs its middleware at the ROOT (gated by path), and root
// middleware runs for the routes registered AFTER it — fiber's own semantics,
// unchanged by scoping. So `Use` before the routes guards them, and `Use` after
// them guards nothing. It composes either way: zip's inert-middleware rule asks
// whether the NODE has routes beneath it, and the root always does, so a
// late Use is a seam no compose check can refuse.
//
// This is not a regression — a group whose subtree was empty gated nothing in
// either order. It is the residue: the one dead-guard shape that survives, and
// the reason it is written down here is that the next person to write
// `g.Use(auth)` under their routes will get a green build and an open door.
//
// It is also load-bearing, not hypothetical. apps/company registers
// /v1/company/fundraise/deck ABOVE its `g.Use(limitBody)` on purpose: the deck
// is document bytes, not JSON, and a JSON body cap on it would be wrong. That
// deliberate exemption is spelled out in a comment and depended on by nothing
// else — so the rule it rests on is asserted here, where changing it goes red.
func TestScopedUseGatesOnlyWhatFollowsIt(t *testing.T) {
	app := newApp()
	err := mountAll(t, app, []cloud.Plugin{
		{Name: "early", Mount: func(r cloud.Router, _ cloud.Deps) error {
			g := r.Group("/v1/early")
			g.Use(zip.H(deny))
			g.Get("/x", pong)
			return nil
		}},
		{Name: "late", Mount: func(r cloud.Router, _ cloud.Deps) error {
			g := r.Group("/v1/late")
			g.Get("/x", pong)
			g.Use(zip.H(deny))
			return nil
		}},
	})
	if err != nil {
		t.Fatalf("MountAll: %v", err)
	}
	if got := get(t, app, "/v1/early/x"); got != http.StatusUnauthorized {
		t.Errorf("/v1/early/x = %d, want 401 — Use before the routes must guard them", got)
	}
	if got := get(t, app, "/v1/late/x"); got != http.StatusOK {
		t.Errorf("/v1/late/x = %d, want 200 — Use after the routes reaches nothing; if this is now 401 the rule changed and the doc above is stale", got)
	}
}
