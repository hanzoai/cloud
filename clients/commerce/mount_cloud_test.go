// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestCommerceMount_HealthAndGinSurface boots Mount on a fresh zip.App and proves
// both (a) the native /_/commerce/healthz route answers 200 (independent of the gin
// engine, so probes survive a router outage) and (b) the embedded gin engine is
// reachable through the outer zip.App — routing /v1/commerce/tenant through
// zip → AdaptNetHTTP → gin reaches a real handler, not the SPA NoRoute fallthrough.
func TestCommerceMount_HealthAndGinSurface(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	emb, err := Mount(app, MountConfig{Brand: "hanzo", Env: "devnet", Domain: "api.hanzo.ai", DataDir: t.TempDir()}, luxlog.New("test"))
	if err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if emb == nil {
		t.Fatal("Mount returned nil Embedded (embed failed)")
	}

	// (a) native health route
	resp, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, "/_/commerce/healthz", nil))
	if err != nil {
		t.Fatalf("health Test: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), `"service":"commerce"`) {
		t.Fatalf("healthz body = %q, want service=commerce", string(body))
	}

	// (b) gin surface reachable — a NoRoute SPA body proves the request never
	// reached a real gin handler.
	req := httptest.NewRequest(http.MethodGet, "/v1/commerce/tenant", nil)
	req.Host = "pay.example.test"
	resp2, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("tenant Test: %v", err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	_ = resp2.Body.Close()
	if string(body2) == `{"error":"not found"}` {
		t.Fatalf("/v1/commerce/tenant fell through to NoRoute SPA — gin surface not wired through zip mount (status=%d)", resp2.StatusCode)
	}
}

// TestCommerceMountFailClosed503 proves the fail-soft path: mountFailClosed serves an
// honest JSON 503 on every commerce prefix instead of letting /v1/commerce/* fall
// through to another subsystem's catch-all. cloud + every co-resident subsystem stay up.
func TestCommerceMountFailClosed503(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	mountFailClosed(app)
	for _, p := range []string{"/v1/commerce/tenant", "/v1/commerce/anything", "/_/commerce/admin"} {
		resp, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, p, nil))
		if err != nil {
			t.Fatalf("Test(%s): %v", p, err)
		}
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s = %d, want 503 (fail-closed)", p, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
}

// TestCommerceMountNilGuards proves Mount fails closed on a nil app or nil logger
// before it does any work.
func TestCommerceMountNilGuards(t *testing.T) {
	if _, err := Mount(nil, MountConfig{}, luxlog.New("test")); err == nil {
		t.Error("Mount(nil app) must error")
	}
	if _, err := Mount(zip.New(zip.Config{Logger: luxlog.New("test")}), MountConfig{}, nil); err == nil {
		t.Error("Mount(nil logger) must error")
	}
}
