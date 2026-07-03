package main

// Real integration tests for the unified Hanzo Cloud binary (HIP-0106).
// These exercise the actual composition root — the SAME wiring serve.go performs:
// the explicitly-mounted subsystems (kms.New + kms.Mount, metrics.Mount) THEN
// BuildDeps -> MountAll over the init()-populated Registry -> serve via the real
// zip/fiber stack. app.Fiber().Test drives requests in-process, no listener.

import (
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	kms "github.com/hanzoai/cloud/clients/kms"
	"github.com/hanzoai/metrics"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// Registry-mounted subsystems that must self-register via init(). kms + metrics
// are NO LONGER here: they are the first two converted to the HIP-0106 explicit
// New/Mount model and are composed directly by serve.go / newTestApp, not via the
// registry. This list shrinks toward empty as the migration fans out.
var wantSubsystems = []string{
	"base", "authz", "o11y",
	"licensing", "plans", "pricing", "ai",
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
	// kms + metrics must NOT be in the registry — they are explicitly composed.
	for _, name := range []string{"kmssvc", "metrics"} {
		if got[name] {
			t.Errorf("subsystem %q must be explicitly composed, not registry-registered", name)
		}
	}
	t.Logf("registry assembled %d subsystems", len(cloud.Registry))
}

// newTestApp mirrors serve.go's composition root: the explicitly-wired subsystems
// (kms, metrics) via their New/Mount, then BuildDeps + MountAll for the registry.
// This is the boot-parity harness — it wires exactly what the binary wires.
func newTestApp(t *testing.T, enable ...string) *zip.App {
	t.Helper()
	cfg := &cloud.Config{
		Brand:   "hanzo",
		Domain:  "api.hanzo.ai",
		DataDir: t.TempDir(),
		Enable:  enable,
	}
	deps := cloud.BuildDeps(cfg)

	// Explicit KMS construction + bridge (mirrors serve.go).
	var kmsStore *kms.Client
	if cfg.Enabled("kmssvc") {
		s, err := kms.New(kms.Config{DataDir: cfg.DataDir, MasterKeyB64: cfg.KMSMasterKeyRef}, deps.Logger)
		if err != nil {
			t.Fatalf("kms.New: %v", err)
		}
		kmsStore = s
		deps.KMS = s
	}

	app := zip.New(zip.Config{Logger: deps.Logger})
	app.Use(middleware.Recover())
	app.Use(middleware.RequestID())
	app.Use(middleware.Logger(deps.Logger))

	// Explicitly-wired subsystems (mirrors serve.go), then the registry.
	if cfg.Enabled("kmssvc") {
		if err := kms.Mount(app, kms.Deps{Store: kmsStore, Logger: deps.Logger, Brand: cfg.Brand, Env: cfg.Env, IAMIssuer: cfg.IAMIssuer}); err != nil {
			t.Fatalf("kms.Mount: %v", err)
		}
	}
	if cfg.Enabled("metrics") {
		if err := metrics.Mount(app, metrics.Deps{Logger: deps.Logger, DataDir: cfg.DataDir, Brand: cfg.Brand}); err != nil {
			t.Fatalf("metrics.Mount: %v", err)
		}
	}
	if err := cloud.MountAll(app, cfg, deps); err != nil {
		t.Fatalf("MountAll(%v): %v", enable, err)
	}
	return app
}

// The self-contained subsystems mount in-process (per-tenant SQLite / in-mem,
// HIP-0302) and serve a healthy /v1/<name>/health with no external deps. This
// spans BOTH mount paths: metrics (explicitly composed) + base/authz/plans/pricing
// (registry) all answer 200 through the SAME app.
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

// TestExplicitKMSHealthFailsClosed: the explicitly-composed kms subsystem serves
// its REAL /v1/kms/health probe through the composition root — 503 (health-only)
// with no master key, never a fake 200. This is the boot-parity proof for the
// explicit-wiring path.
func TestExplicitKMSHealthFailsClosed(t *testing.T) {
	app := newTestApp(t, "kmssvc")
	req := httptest.NewRequest("GET", "/v1/kms/health", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("GET /v1/kms/health: %v", err)
	}
	if resp.StatusCode != 503 {
		t.Errorf("GET /v1/kms/health (no key) = %d, want 503 (fail-closed health-only)", resp.StatusCode)
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
