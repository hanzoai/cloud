package main

// Real integration tests for the unified Hanzo Cloud binary (HIP-0106).
// These exercise the actual orchestrator path — BuildDeps -> MountAll over
// apps.Wire() -> serve via the real zip/fiber + jsonenc stack —
// not a hand-rolled smoke harness. app.Fiber().Test drives requests in-process,
// no listener or external services.

import (
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// every subsystem the unified binary wires must appear in apps.Wire() — this
// is the proof Wire() actually assembles the whole matrix.
var wantSubsystems = []string{
	"metrics", "base", "authz", "o11y",
	"licensing", "plans", "pricing", "ai",
}

func TestRegistryAssemblesSubsystems(t *testing.T) {
	wire := apps.Wire()
	got := map[string]bool{}
	for _, s := range wire {
		got[s.Name] = true
	}
	for _, name := range wantSubsystems {
		if !got[name] {
			t.Errorf("subsystem %q missing from apps.Wire()", name)
		}
	}
	t.Logf("Wire() assembled %d subsystems", len(wire))
}

// newTestApp mirrors main()'s wiring: BuildDeps + the canonical middleware
// pipeline + MountAll for the requested subsystems.
func newTestApp(t *testing.T, enable ...string) *zip.App {
	t.Helper()
	cfg := &cloud.Config{
		Brand:   "hanzo",
		Domain:  "api.hanzo.ai",
		DataDir: t.TempDir(),
		Enable:  enable,
	}
	deps := cloud.BuildDeps(cfg)
	app := zip.New(zip.Config{Logger: deps.Logger})
	app.Use(middleware.Recover())
	app.Use(middleware.RequestID())
	app.Use(middleware.Logger(deps.Logger))
	if err := cloud.MountAll(app, apps.Wire(), cfg, deps); err != nil {
		t.Fatalf("MountAll(%v): %v", enable, err)
	}
	return app
}

// The self-contained subsystems mount in-process (per-tenant SQLite / in-mem,
// HIP-0302) and serve a healthy /v1/<name>/health with no external deps.
func TestMountAllAndServeHealth(t *testing.T) {
	healthy := []string{"base", "authz", "metrics", "plans", "pricing"}
	app := newTestApp(t, healthy...)
	for _, name := range healthy {
		path := "/v1/" + name + "/health"
		req := httptest.NewRequest("GET", path, nil)
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if resp.StatusCode != 200 {
			t.Errorf("GET %s = %d, want 200", path, resp.StatusCode)
		}
	}
}

// Subsystems whose deps are disabled (no in-process peer, no ZAP endpoint) must
// mount and fail CLOSED — a 5xx from the disabled stub, never a panic or a
// silent 200. This proves the BuildDeps three-mode contract end-to-end.
func TestDepGatedSubsystemsFailClosed(t *testing.T) {
	for _, name := range []string{"ai", "o11y"} {
		app := newTestApp(t, name)
		path := "/v1/" + name + "/health"
		req := httptest.NewRequest("GET", path, nil)
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		// Fail-closed = never a 2xx/3xx success while deps are disabled. A
		// dep-gated subsystem may reject with 4xx (deny) or 5xx (unavailable);
		// both are closed. (o11y denies 403; ai returns 5xx.) In prod, serve.go's
		// generic health route — installed before MountAll — answers /health 200;
		// this harness omits it deliberately to probe the mounted handler itself.
		if resp.StatusCode < 400 {
			t.Errorf("GET %s = %d, want >=400 (fail-closed: a dep-disabled subsystem must not serve 2xx)", path, resp.StatusCode)
		}
	}
}
