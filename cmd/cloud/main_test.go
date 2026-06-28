package main

// Real integration tests for the unified Hanzo Cloud binary (HIP-0106).
// These exercise the actual orchestrator path — BuildDeps -> MountAll over the
// init()-populated Registry -> serve via the real zip/fiber + jsonenc stack —
// not a hand-rolled smoke harness. app.Fiber().Test drives requests in-process,
// no listener or external services.

import (
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/zip"
	"github.com/hanzoai/zip/middleware"
)

// every subsystem main.go imports must self-register via init() — this is the
// proof the unified binary actually wires the whole matrix.
var wantSubsystems = []string{
	"kms", "metrics", "iam", "base", "authz", "o11y",
	"gateway", "licensing", "plans", "pricing", "ai", "mcp",
}

func TestRegistryAssemblesSubsystems(t *testing.T) {
	got := map[string]bool{}
	for _, s := range cloud.Registry {
		got[s.Name] = true
	}
	for _, name := range wantSubsystems {
		if !got[name] {
			t.Errorf("subsystem %q not registered — main.go import or its init() missing", name)
		}
	}
	t.Logf("registry assembled %d subsystems", len(cloud.Registry))
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
	if err := cloud.MountAll(app, cfg, deps); err != nil {
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
		if resp.StatusCode < 500 {
			t.Errorf("GET %s = %d, want >=500 (fail-closed; deps disabled)", path, resp.StatusCode)
		}
	}
}
