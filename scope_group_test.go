package cloud_test

// scope.Group is the third door, and it is the one that shipped broken twice in
// one day. Each test below is the smallest program that reproduces one of those
// failures. They share scope_test.go's helpers (deny, pong, get, mountAll,
// newApp) — same package, one set of primitives.
//
// The two failures are OPPOSITE KINDS, which is why one test cannot cover both:
//
//   - It DOES NOT COMPOSE. Group handed back the raw zip router, so `g.Use(mw)`
//     went past every confinement in scope.go and hung middleware on a node whose
//     subtree is necessarily empty — a subsystem's routes register through
//     ZipApp, on the ROOT. zip refuses that at boot, so fifteen plugins built,
//     linked, passed vet and unit tests, and crash-looped in production.
//     Detected here by [zip.App.Build], which is `Listen` minus the sockets and
//     returns the verdict instead of panicking with it.
//
//   - It COMPOSES PERFECTLY AND IS WRONG. When Group became a child scope, the
//     route methods and OpScope dropped the prefix, so
//     `zip.Get(app.Group("/v1"), "/bots", h)` registered `/bots`. A route
//     silently MOVED. Nothing refuses that — the program is valid, it just
//     answers somewhere else — so no compose check anywhere could have caught
//     it, and only an assertion on the composed ROUTE TABLE can.

import (
	"context"
	"net/http"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// none/ok are the smallest typed op there is. A TYPED op is the point: it
// registers through OpScope rather than through a route method, which is the
// path that dropped the prefix and the reason a group's node is empty.
type none struct{}
type ok struct {
	OK bool `json:"ok"`
}

func okOp(context.Context, *none) (*ok, error) { return &ok{OK: true}, nil }

// patterns is the COMPOSED route table — what the program will actually answer,
// asked of the program rather than of the registrations that built it.
func patterns(t *testing.T, app *zip.App) []string {
	t.Helper()
	if err := app.Build(); err != nil {
		t.Fatalf("app.Build: %v", err)
	}
	var out []string
	for _, r := range app.Routes() {
		// zip mounts its own plumbing (/zap, the MCP door). This test is about
		// where a SUBSYSTEM's routes land, so keep the /v1 surface.
		if strings.HasPrefix(r.Pattern, "/v1") {
			out = append(out, r.Method+" "+r.Pattern)
		}
	}
	sort.Strings(out)
	return out
}

// TestGroupWithLaterUseComposes is the outage, in nine lines.
//
// A subsystem takes a group and hangs middleware on it AFTERWARDS — `g :=
// app.Group(p); g.Use(mw)`, the idiom scope.go once documented as needing no
// policing — while its routes register at full paths through ZipApp, because
// that is where a typed op MUST go: the op registry lives on the concrete
// *zip.App and there is exactly one. Same paths, different nodes.
//
// Group returning the raw router makes this program invalid, and invalid at
// BOOT: zip walks the tree, finds middleware at /v1/avatar with nothing beneath
// it, and refuses. `go build` sees nothing wrong; the unit tests see nothing
// wrong; the pod crash-loops.
func TestGroupWithLaterUseComposes(t *testing.T) {
	app := newApp()
	err := mountAll(t, app, []cloud.Plugin{
		{Name: "avatar", Mount: func(r cloud.Router, _ cloud.Deps) error {
			g := r.Group("/v1/avatar") // no middleware yet — the group is bare
			g.Use(zip.H(deny))         // …and it arrives here, after the fact
			// Through ZipApp: the ROOT. Nothing lands under the group node.
			zip.Get(cloud.ZipApp(r), "/v1/avatar/me", okOp)
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

	// THE assertion. Build is Listen without the sockets: same walk, same
	// validations, same verdict — returned rather than thrown.
	if err := app.Build(); err != nil {
		t.Fatalf("the program does not compose, so the binary panics at boot:\n\t%v", err)
	}

	// Composing is necessary and not sufficient: the middleware must still be
	// the subsystem's own, wrapping its own subtree and nobody else's.
	if got := get(t, app, "/v1/avatar/me"); got != http.StatusUnauthorized {
		t.Errorf("/v1/avatar/me = %d, want 401 — the group's middleware must run beneath it", got)
	}
	if got := get(t, app, "/v1/neighbour/ping"); got != http.StatusOK {
		t.Errorf("/v1/neighbour/ping = %d, want 200 — a group's middleware must not reach a neighbour", got)
	}
}

// TestGroupPrefixesWhatRegistersThroughIt pins the second failure: a group that
// composes and lies. A child Group must PREFIX what registers through it — that
// is what a group IS — down BOTH paths, because a subsystem uses both:
//
//	v1.Get("/x", h)              // the route method
//	zip.Get(v1, "/bots", op)     // OpScope — apps/bot and apps/entitlements
//
// and it must leave an absolute path registered at the subsystem ROOT alone,
// which is what every other subsystem writes.
//
// Asserted on the composed table and not on a request, because the failure is
// not a broken program: /bots answers perfectly well at /bots. The only witness
// is the address itself.
func TestGroupPrefixesWhatRegistersThroughIt(t *testing.T) {
	app := newApp()
	err := mountAll(t, app, []cloud.Plugin{
		{Name: "bots", Prefixes: []string{"/v1"}, Mount: func(r cloud.Router, _ cloud.Deps) error {
			v1 := r.Group("/v1")
			zip.Get(v1, "/bots", okOp)         // typed: through OpScope
			v1.Get("/bots/plain", pong)        // untyped: through the route method
			v1.Group("/guide").Get("/x", pong) // nested groups concatenate
			r.Get("/v1/bots/abs", pong)        // the root: absolute, untouched
			return nil
		}},
	})
	if err != nil {
		t.Fatalf("MountAll: %v", err)
	}

	want := []string{
		"GET /v1/bots",       // NOT /bots — the OpScope carries the group's prefix
		"GET /v1/bots/abs",   // the root is the identity
		"GET /v1/bots/plain", // NOT /bots/plain
		"GET /v1/guide/x",    // NOT /x, and not /guide/x
	}
	got := patterns(t, app)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("the composed route table moved:\n got %v\nwant %v", got, want)
	}

	// And they are reachable at the addresses the table claims, so this is the
	// program's behaviour and not a projection that agrees with itself.
	for _, p := range []string{"/v1/bots", "/v1/bots/abs", "/v1/bots/plain", "/v1/guide/x"} {
		if s := get(t, app, p); s != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", p, s)
		}
	}
}

// TestGroupUseOutsideThePrefixesFailsTheMount is confinement through the door
// Group opened. scope_test.go already covers `r.Group(p, mw)` — middleware
// passed INTO Group. This is the other spelling, `g := r.Group(p); g.Use(mw)`,
// which is the one that used to bypass confinement entirely by handing back the raw
// router. Nothing is installed and the mount fails, so the binary refuses to
// boot half-wrapped rather than serving under a stranger's middleware.
func TestGroupUseOutsideThePrefixesFailsTheMount(t *testing.T) {
	app := newApp()
	err := mountAll(t, app, []cloud.Plugin{
		{Name: "neighbour", Mount: func(r cloud.Router, _ cloud.Deps) error {
			r.Get("/v1/neighbour/ping", pong)
			return nil
		}},
		{Name: "greedy", Mount: func(r cloud.Router, _ cloud.Deps) error {
			g := r.Group("/v1/neighbour") // legal: a prefix is just a path
			g.Use(zip.H(deny))            // NOT legal: it reaches where greedy may not
			return nil
		}},
	})
	if err == nil {
		t.Fatal("MountAll succeeded — greedy wrapped a neighbour's subtree without declaring it")
	}
	if !strings.Contains(err.Error(), "/v1/neighbour") {
		t.Errorf("error does not name the escape: %v", err)
	}
	if got := get(t, app, "/v1/neighbour/ping"); got != http.StatusOK {
		t.Errorf("neighbour = %d, want 200 — the refused middleware was installed anyway", got)
	}
}

// TestBareGroupOutsideThePrefixesIsAllowed keeps the refusal from overreaching.
// A PREFIX IS JUST A PATH: scope bounds middleware and has never bounded route
// registration, so a subsystem grouping routes under someone else's tree composes
// a router the way routers are built, and refusing the bare group would fail that
// mount. Every app answering under its own name (HIP-0139 §3) is the goal, not
// something this client is entitled to enforce — the misfiled ratchet does that, in
// one place, against the served document.
//
// It is the Use that is the escape, which is exactly why the two tests are
// separate.
func TestBareGroupOutsideThePrefixesIsAllowed(t *testing.T) {
	app := newApp()
	err := mountAll(t, app, []cloud.Plugin{
		{Name: "thing", Mount: func(r cloud.Router, _ cloud.Deps) error {
			elsewhere := r.Group("/v1/somewhere") // outside /v1/thing, and fine
			elsewhere.Get("/:id/thing", pong)
			r.Get("/v1/thing", pong)
			return nil
		}},
	})
	if err != nil {
		t.Fatalf("MountAll: %v — a bare group outside the prefixes is a path, not middleware", err)
	}
	if got := patterns(t, app); len(got) != 2 || got[0] != "GET /v1/somewhere/:id/thing" || got[1] != "GET /v1/thing" {
		t.Errorf("routes = %v, want the two the subsystem registered", got)
	}
	if got := get(t, app, "/v1/somewhere/acme/thing"); got != http.StatusOK {
		t.Errorf("/v1/somewhere/acme/thing = %d, want 200", got)
	}
}
