// Copyright © 2026 Hanzo AI. MIT License.

//go:build cloud
// +build cloud

package commerce

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"

	"github.com/zap-proto/zip"
)

// TestMount_RegistersHealth boots an empty zip.App, runs commerce.Mount,
// and asserts the native /_/commerce/healthz route answers 200 with
// {"service":"commerce"}. Covers:
//
//   - Mount() can be called against a fresh *zip.App
//   - cloud.Register fires from init() (Registry contains "commerce")
//   - the native zip health route reaches Fiber and returns JSON
//
// The legacy gin handler surface (proxied via app.Mount(...)) is
// covered separately by TestMount_GinSurfaceReachable below.
func TestMount_RegistersHealth(t *testing.T) {
	if !registryContains("commerce") {
		t.Fatalf("cloud.Registry missing 'commerce'; Names=%v", registryNames())
	}

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	deps := cloud.Deps{
		Logger:  luxlog.New("test"),
		Brand:   "hanzo",
		Domain:  "api.hanzo.ai",
		DataDir: t.TempDir(),
	}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	req := httptest.NewRequest("GET", "/_/commerce/healthz", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Fiber Test: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status: got %d want 200", resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	body := string(raw)
	if !strings.Contains(body, `"service":"commerce"`) {
		t.Fatalf("body: got %q, want service=commerce", body)
	}
}

// TestMount_GinSurfaceReachable proves the inner gin engine is actually
// reachable through the outer zip.App. The /v1/commerce/tenant public route
// is registered on the embedded gin engine; routing a request through
// zip → AdaptNetHTTP → gin must reach the org-as-tenant handler. We accept
// any non-NoRoute response — org resolution returns 200 with the tenant JSON —
// which proves the route resolved past zip's adapter into a real handler
// rather than falling through to the SPA NoRoute branch.
//
// Without this test, mount.go could regress to "zip mounts the prefix
// but the inner engine is broken" and TestMount_RegistersHealth would
// still pass (it only exercises the native zip route).
func TestMount_GinSurfaceReachable(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	deps := cloud.Deps{
		Logger:  luxlog.New("test"),
		Brand:   "hanzo",
		Domain:  "api.hanzo.ai",
		DataDir: t.TempDir(),
	}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}

	req := httptest.NewRequest("GET", "/v1/commerce/tenant", nil)
	req.Host = "pay.example.test"
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Fiber Test: %v", err)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	body := string(raw)

	// NoRoute SPA fallback for API paths returns exactly the canonical
	// `{"error":"not found"}` body. Seeing that proves the request never
	// reached a real gin handler.
	if body == `{"error":"not found"}` {
		t.Fatalf("/v1/commerce/tenant fell through to NoRoute SPA — gin surface not wired through zip mount. status=%d body=%s", resp.StatusCode, body)
	}
}

func registryContains(name string) bool {
	for _, s := range cloud.Registry {
		if s.Name == name {
			return true
		}
	}
	return false
}

func registryNames() []string {
	out := make([]string, 0, len(cloud.Registry))
	for _, s := range cloud.Registry {
		out = append(out, s.Name)
	}
	return out
}
