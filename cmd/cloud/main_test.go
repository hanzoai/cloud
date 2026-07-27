package main

// Real integration tests for the unified Hanzo Cloud binary (HIP-0106).
// These exercise the actual orchestrator path — BuildDeps -> MountAll over
// apps.Wire() -> serve via the real zip/fiber + jsonenc stack —
// not a hand-rolled smoke harness. app.Fiber().Test drives requests in-process,
// no listener or external services.

import (
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps"
	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// TestMain supplies the two things MountAll(apps.Wire()) needs from its
// environment and cannot invent for itself.
//
// The dev KMS key is the same 32 zero bytes the root package's TestMain and
// clients/{esign,translate} already inject: subsystems with an encrypted store
// (pricing, o11y's annotation queues) refuse to open a data plane unencrypted,
// which is correct, and a test box has no KMS. One dev-only key, one pattern.
//
// The plugin binaries are the cost of a plugin subsystem: they are no longer
// linked in, so the composition root can only mount one by starting the real
// binary. Building them here is deliberate — the alternative, a stub, would let
// these tests pass against a route table no deployment ever serves. Cached after
// the first run. If one cannot be built the dependent tests fail with zip's own
// fork/exec message, which names the missing file.
//
// WHICH binaries is asked of the composition root (cloud.PluginNames over
// apps.Wire()), never listed here: a test with its own list would keep passing
// after someone extracts the next app and forgets to add it, which is precisely
// the failure this harness exists to catch.
func TestMain(m *testing.M) { os.Exit(runTests(m)) }

// runTests exists so the temp dirs are removed on the way out: os.Exit does not
// run deferred functions, so TestMain cannot both clean up and set the code.
func runTests(m *testing.M) int {
	if os.Getenv("CLOUD_KMS_MASTER_KEY_REF") == "" {
		_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=") // dev-only
	}
	// The plugin child inherits this; without it the child would target the
	// production /var/lib/cloud and die opening its store.
	if os.Getenv("CLOUD_DATA_DIR") == "" {
		dir, err := os.MkdirTemp("", "cloud-test-data-")
		if err != nil {
			return 1
		}
		defer os.RemoveAll(dir)
		_ = os.Setenv("CLOUD_DATA_DIR", dir)
	}
	dir, err := os.MkdirTemp("", "cloud-test-plugins-")
	if err != nil {
		return 1
	}
	defer os.RemoveAll(dir)
	for _, name := range cloud.PluginNames(apps.Wire()) {
		env := "CLOUD_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_BIN"
		if os.Getenv(env) != "" {
			continue
		}
		bin := filepath.Join(dir, name)
		// GOROOT/bin/go, not "go": the toolchain that is running this test is the
		// one that must build the plugin, and it is not always on PATH.
		cmd := exec.Command(goTool(), "build", "-o", bin, "./cmd/"+name)
		cmd.Dir = "../.." // the package dir is cmd/cloud
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			os.Stderr.WriteString("TestMain: building the " + name + " plugin failed: " + err.Error() + "\n")
		} else {
			_ = os.Setenv(env, bin)
		}
	}
	code := m.Run()
	// fullyMountedApp deliberately mounts ONCE and shares the app across tests, so
	// no single test may shut it down. It still holds a plugin child, so it is
	// closed here — after the last test — for the same reason newTestApp cleans up.
	if mountedTo != nil {
		_ = mountedTo.Shutdown()
	}
	return code
}

func goTool() string {
	if exe := filepath.Join(runtime.GOROOT(), "bin", "go"); exe != "" {
		if _, err := os.Stat(exe); err == nil {
			return exe
		}
	}
	return "go"
}

// every subsystem the unified binary wires must appear in apps.Wire() — this
// is the proof Wire() actually assembles the whole matrix.
var wantSubsystems = []string{
	"metrics", "base", "authz", "o11y",
	"licensing", "plan", "pricing", "ai",
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
	// Mounting a plugin subsystem starts a CHILD PROCESS, and zip stops it in an
	// OnShutdown hook. A harness that mounts but never shuts down leaks that child
	// for the life of the run — and because zip gives the child the host's stdout,
	// the pipe stays open and `go test` blocks after the last test, then reports
	// FAILURE on a suite that passed. Releasing what we acquired is the fix.
	t.Cleanup(func() { _ = app.Shutdown() })
	return app
}

// The self-contained subsystems mount in-process (per-tenant SQLite / in-mem,
// HIP-0302) and serve a healthy /v1/<name>/health with no external deps.
func TestMountAllAndServeHealth(t *testing.T) {
	// enable id -> the health path its Mount serves. For most, id == route prefix;
	// "plan" is the exception (enable id normalized to match clients/plan, but its
	// product routes — incl. /v1/plans/health — stay under the plural /v1/plans/*).
	healthy := map[string]string{
		"base":    "/v1/base/health",
		"authz":   "/v1/authz/health",
		"metrics": "/v1/metrics/health",
		"plan":    "/v1/plans/health",
		"pricing": "/v1/pricing/health",
	}
	enable := make([]string, 0, len(healthy))
	for name := range healthy {
		enable = append(enable, name)
	}
	app := newTestApp(t, enable...)
	for name, path := range healthy {
		req := httptest.NewRequest("GET", path, nil)
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("enable %q: GET %s: %v", name, path, err)
		}
		if resp.StatusCode != 200 {
			t.Errorf("enable %q: GET %s = %d, want 200", name, path, resp.StatusCode)
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
