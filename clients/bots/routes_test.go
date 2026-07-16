package bots

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/runtime"
	"github.com/hanzoai/cloud/clients/visor"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Regression guard for the /v1/bots route collision.
//
// clients/visor once registered GET /v1/bots for its bot MACHINES and this
// package registered GET /v1/bots for bot RUNS. The router resolves byte-identical
// patterns by first-registration — silently, with no panic — and visor mounts
// first (apps.Wire: visor, then runtime, then bots), so visor's machine list answered
// the console's run list and this package's handler was unreachable. Two values
// sharing one name, one namespace.
//
// These tests pin the fix from both ends: structurally (no two subsystems may
// register the same method+path) and behaviourally (GET /v1/bots serves RUNS),
// with the subsystems mounted in the real Wire order that produced the bug.

// stubRuntime stands in for the bot runtime, serving its documented contract and
// recording the paths + org it was asked with. Real Mounts talk to it through the
// real transport, so these tests exercise the shipping path end to end.
type stubRuntime struct {
	mu    sync.Mutex
	runs  map[string][]map[string]any // org -> rows
	paths []string
	orgs  []string
}

func (s *stubRuntime) start(t *testing.T) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/bots", func(w http.ResponseWriter, r *http.Request) {
		org := r.Header.Get("X-Org-Id")
		s.mu.Lock()
		s.paths, s.orgs = append(s.paths, r.URL.Path), append(s.orgs, org)
		rows := s.runs[org]
		s.mu.Unlock()
		if rows == nil {
			rows = []map[string]any{}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"bots": rows})
	})
	mux.HandleFunc("/v1/bots/", func(w http.ResponseWriter, r *http.Request) { // {id}/stop
		s.mu.Lock()
		s.paths, s.orgs = append(s.paths, r.URL.Path), append(s.orgs, r.Header.Get("X-Org-Id"))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"stopped"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("BOT_GATEWAY_URL", srv.URL)
}

func (s *stubRuntime) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...)
}

// mountFleet mounts, in apps.Wire order, the three subsystems that shared the
// /v1/bot* namespace, over a stub runtime.
func mountFleet(t *testing.T, rt *stubRuntime) *zip.App {
	t.Helper()
	t.Setenv(gatewayURLEnv, "https://bot.example.test")
	rt.start(t)
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}
	if err := visor.Mount(app, deps); err != nil { // Wire order: visor first — the shadowing mount
		t.Fatalf("visor.Mount: %v", err)
	}
	if err := runtime.Mount(app, deps); err != nil {
		t.Fatalf("runtime.Mount: %v", err)
	}
	if err := Mount(app, deps); err != nil { // …bots last, as in Wire
		t.Fatalf("bots.Mount: %v", err)
	}
	return app
}

// collisions reports every route claimed by more than one registration.
//
// Counting GetRoutes() entries is NOT enough and looking only at that is how this
// guard was blind at first: the router MERGES byte-identical patterns into ONE
// Route carrying both handlers chained, so the second registration leaves the
// entry count at one and shows up only as a handler count above one. Every real
// route in the fleet registers exactly one handler, so >1 is the collision. The
// entry count is kept as well, for any overlap the router does not merge.
func collisions(app *zip.App) []string {
	var out []string
	seen := map[string]int{}
	for _, r := range app.Fiber().GetRoutes() {
		key := r.Method + " " + r.Path
		if n := len(r.Handlers); n > 1 {
			out = append(out, fmt.Sprintf("%s — %d handlers chained on one route", key, n))
			continue
		}
		if seen[key]++; seen[key] > 1 {
			out = append(out, fmt.Sprintf("%s — %d separate registrations", key, seen[key]))
		}
	}
	return out
}

// The guard itself must be shown to FIRE on the bug, or it is decoration. This
// reproduces the original collision — two subsystems, one pattern — and asserts
// collisions() sees it, through the SAME function the real check below uses.
func TestDuplicateRouteGuardDetectsACollision(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	app.Get("/v1/bots", func(c *zip.Ctx) error { return c.JSON(200, map[string]any{"bots": []string{"machine"}}) })
	app.Get("/v1/bots", func(c *zip.Ctx) error { return c.JSON(200, map[string]any{"bots": []string{"run"}}) })

	got := collisions(app)
	if len(got) == 0 {
		t.Fatal("the guard cannot see the very collision it guards: two GET /v1/bots handlers are registered and collisions() reported none")
	}
	if !strings.Contains(got[0], "GET /v1/bots") {
		t.Fatalf("guard named the wrong route: %v", got)
	}
}

// No two subsystems may claim the same method+path. The router does not panic on
// a byte-identical duplicate — it silently merges and keeps the first — so
// nothing but this catches the class of bug.
func TestSubsystemsDoNotRegisterDuplicateRoutes(t *testing.T) {
	for _, c := range collisions(mountFleet(t, &stubRuntime{})) {
		t.Errorf("route collision: %s — one handler silently shadows the other", c)
	}
}

// The behavioural half: GET /v1/bots serves bot RUNS from the runtime. The run the
// runtime holds must come back out — which it cannot do if visor's machine list is
// answering this route, since a machine list never asks the runtime anything.
func TestGetBotsServesRunsNotMachines(t *testing.T) {
	rt := &stubRuntime{runs: map[string][]map[string]any{
		"acme": {{"runId": "run_1", "task": "prove the route", "surface": "desktop", "status": "running", "startedAt": "2023-11-14T22:13:20Z"}},
	}}
	app := mountFleet(t, rt)

	code, body := call(t, app, http.MethodGet, "/v1/bots", "acme")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/bots want 200, got %d (%s)", code, body)
	}
	var v botsView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if len(v.Bots) != 1 || v.Bots[0].RunID != "run_1" {
		t.Fatalf("GET /v1/bots must serve the org's runs, got %s", body)
	}
	// A run row, not a machine row: the fields the console reads are populated.
	if v.Bots[0].Task != "prove the route" || v.Bots[0].SessionURL != "https://bot.example.test/vnc?nodeId=run_1" {
		t.Fatalf("row is not a run: %+v", v.Bots[0])
	}
	// It reached the RUNTIME — the only place a run has ever existed.
	if got := rt.seen(); len(got) != 1 || got[0] != "/v1/bots" {
		t.Fatalf("bots must read the runtime's run list, saw %v", got)
	}
}

// The bot MACHINE surface still exists — under visor's own namespace, where a
// machine belongs. It is reachable and it is NOT /v1/bots.
func TestBotMachineSurfaceMovedToCompute(t *testing.T) {
	app := mountFleet(t, &stubRuntime{})

	paths := map[string]bool{}
	for _, r := range app.Fiber().GetRoutes() {
		paths[r.Method+" "+r.Path] = true
	}
	for _, want := range []string{
		"GET /v1/compute/bots",
		"POST /v1/compute/bots/launch",
		"GET /v1/compute/bots/:id",
		"DELETE /v1/compute/bots/:id",
		"POST /v1/compute/bots/:id/:action",
	} {
		if !paths[want] {
			t.Errorf("bot machine route %q is missing", want)
		}
	}
	// …and nothing but the run control plane claims /v1/bots.
	for _, gone := range []string{"GET /v1/bots/:id", "DELETE /v1/bots/:id", "POST /v1/bots/launch", "POST /v1/bots/:id/:action"} {
		if paths[gone] {
			t.Errorf("machine route %q still squats on the run namespace", gone)
		}
	}
}

// The three values keep three namespaces: runs at /v1/bots, machines under
// /v1/compute/bots, and the runtime passthrough at /v1/bot/*. Nothing in the run
// namespace may be a wildcard, which would swallow every run id.
func TestRunNamespaceHasNoWildcard(t *testing.T) {
	app := mountFleet(t, &stubRuntime{})
	for _, r := range app.Fiber().GetRoutes() {
		if r.Path == "/v1/bots/*" {
			t.Fatalf("%s /v1/bots/* would swallow every run id", r.Method)
		}
	}
}

// The runtime ops face is a SIBLING namespace, not a parent: /v1/bot/* must never
// match a /v1/bots path, or the control plane would be relayed away.
//
// The discriminator is the op the runtime is asked for. bots' stub addresses
// /v1/bots/{id}/stop; the ops face strips its own /v1/bot prefix and would ask for
// something else entirely. So the path the runtime SAW names which handler ran.
func TestRuntimeOpsFaceDoesNotSwallowTheRunNamespace(t *testing.T) {
	rt := &stubRuntime{}
	app := mountFleet(t, rt)

	if code, body := call(t, app, http.MethodPost, "/v1/bots/run_1/stop", "acme"); code != http.StatusOK {
		t.Fatalf("stop want 200, got %d (%s)", code, body)
	}
	if got := rt.seen(); len(got) != 1 || got[0] != "/v1/bots/run_1/stop" {
		t.Fatalf("the run namespace must be served by bots' own stub, runtime saw %v", got)
	}
}
