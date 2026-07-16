package bots

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/agents"
	"github.com/hanzoai/cloud/clients/bot"
	"github.com/hanzoai/cloud/clients/visor"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Regression guard for the /v1/bots route collision.
//
// clients/visor once registered GET /v1/bots for its bot MACHINES and this
// package registered GET /v1/bots for bot RUNS. The router resolves byte-identical
// patterns by first-registration — silently, with no panic — and visor mounts
// first (apps.Wire: visor, then bot, then bots), so visor's machine list answered
// the console's run list and this package's handler was unreachable. Two values
// sharing one name, one namespace.
//
// These tests pin the fix from both ends: structurally (no two subsystems may
// register the same method+path) and behaviourally (GET /v1/bots serves RUNS),
// with the subsystems mounted in the real Wire order that produced the bug.

// mountFleet mounts, in apps.Wire order, the three subsystems that shared the
// /v1/bot* namespace, plus the session plane the run registry reads through.
func mountFleet(t *testing.T) *zip.App {
	t.Helper()
	t.Setenv(gatewayURLEnv, "https://bot.example.test")
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}
	if err := agents.Mount(app, deps); err != nil {
		t.Fatalf("agents.Mount: %v", err)
	}
	if err := visor.Mount(app, deps); err != nil { // Wire order: visor first — the shadowing mount
		t.Fatalf("visor.Mount: %v", err)
	}
	if err := bot.Mount(app, deps); err != nil {
		t.Fatalf("bot.Mount: %v", err)
	}
	if err := Mount(app, deps); err != nil { // …bots last, as in Wire
		t.Fatalf("bots.Mount: %v", err)
	}
	return app
}

// No two subsystems may claim the same method+path. The router does not panic on
// a byte-identical duplicate — it silently keeps the first — so nothing but a
// test catches this class of bug.
func TestSubsystemsDoNotRegisterDuplicateRoutes(t *testing.T) {
	app := mountFleet(t)

	seen := map[string]int{}
	for _, r := range app.Fiber().GetRoutes() {
		seen[r.Method+" "+r.Path]++
	}
	for route, n := range seen {
		if n > 1 {
			t.Errorf("route %s registered %d times — one handler silently shadows the other", route, n)
		}
	}
}

// The behavioural half: GET /v1/bots serves bot RUNS. A launched run must come
// back out of the list — which it cannot do if visor's machine list is answering
// this route, since a machine list has no knowledge of runs.
func TestGetBotsServesRunsNotMachines(t *testing.T) {
	app := mountFleet(t)
	id := launch(t, app, "acme", "prove the route", "desktop")

	code, body := call(t, app, http.MethodGet, "/v1/bots", "acme")
	if code != http.StatusOK {
		t.Fatalf("GET /v1/bots want 200, got %d (%s)", code, body)
	}
	var v botsView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if len(v.Bots) != 1 || v.Bots[0].RunID != id {
		t.Fatalf("GET /v1/bots must serve the org's runs (want %s), got %s", id, body)
	}
	// A run row, not a machine row: the fields the console reads are populated.
	if v.Bots[0].Task != "prove the route" || v.Bots[0].SessionURL == "" {
		t.Fatalf("row is not a run: %+v", v.Bots[0])
	}
}

// The bot MACHINE surface still exists — under visor's own namespace, where a
// machine belongs. It is reachable and it is NOT /v1/bots.
func TestBotMachineSurfaceMovedToCompute(t *testing.T) {
	app := mountFleet(t)

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
	app := mountFleet(t)
	for _, r := range app.Fiber().GetRoutes() {
		if r.Path == "/v1/bots/*" {
			t.Fatalf("%s /v1/bots/* would swallow every run id", r.Method)
		}
	}
}

// The runtime passthrough is a sibling namespace, not a parent: /v1/bot/* must
// never match a /v1/bots path, or the control plane would be proxied away.
//
// The discriminator is the own-key guard, which only the native handler has: a
// run belonging to ANOTHER org is refused 404 here, decided locally. A relay
// would instead forward the request and report on the (absent) runtime, so any
// answer but 404 means /v1/bots/:runId/stop is no longer served natively.
func TestRuntimePassthroughDoesNotSwallowTheRunNamespace(t *testing.T) {
	app := mountFleet(t)
	victim := launch(t, app, "acme", "still mine", "desktop")

	code, body := call(t, app, http.MethodPost, fmt.Sprintf("/v1/bots/%s/stop", victim), "globex")
	if code != http.StatusNotFound {
		t.Fatalf("POST /v1/bots/:runId/stop must be served natively (404 for a foreign run), got %d (%s)", code, body)
	}
}
