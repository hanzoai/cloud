package cloud

// Tests for the edge policy middleware (middleware_edge.go): browser CORS and the
// per-client-IP flood cap. They drive real requests through the zip/fiber stack,
// so the whole path runs end-to-end (header set, preflight short-circuit, 429).

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/gatewaypolicy"
	"github.com/zap-proto/zip"
)

// staticPol builds a static-only (no SQLite) policy store for the middleware tests.
func staticPol(t *testing.T, p gatewaypolicy.Policy) *gatewaypolicy.Store {
	t.Helper()
	s, _ := gatewaypolicy.New("", "admin", p) // "" dataDir ⇒ static-only, no file.
	return s
}

// ── originMatcher ────────────────────────────────────────────────────────────

func TestOriginMatcher(t *testing.T) {
	if newOriginMatcher(nil) != nil {
		t.Fatal("empty allowlist must yield a nil matcher (CORS owned elsewhere)")
	}
	m := newOriginMatcher([]string{"https://app.example.com", "hanzo.chat", "*.hanzo.ai"})
	if m == nil {
		t.Fatal("non-empty allowlist must compile")
	}
	cases := []struct {
		origin string
		want   bool
	}{
		{"https://app.example.com", true},  // exact origin
		{"http://app.example.com", false},  // exact is scheme-specific
		{"https://hanzo.chat", true},       // bare host, any scheme
		{"http://hanzo.chat", true},        // bare host, any scheme
		{"https://hanzo.ai", true},         // wildcard apex
		{"https://console.hanzo.ai", true}, // wildcard subdomain
		{"http://console.hanzo.ai", true},  // wildcard is scheme-agnostic
		{"https://a.b.hanzo.ai", true},     // wildcard nested subdomain
		{"https://evil.com", false},        // not listed
		{"https://nothanzo.ai", false},     // suffix must be dot-bounded
		{"https://hanzo.ai.evil.com", false},
		{"garbage", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := m.allowed(tc.origin); got != tc.want {
			t.Errorf("allowed(%q) = %v, want %v", tc.origin, got, tc.want)
		}
	}
}

// ── EdgeCORS ─────────────────────────────────────────────────────────────────

// corsApp mounts EdgeCORS for the given origins with a trivial /probe handler.
func corsApp(t *testing.T, origins []string) *zip.App {
	app := zip.New(zip.Config{})
	app.Use(EdgeCORS(staticPol(t, gatewaypolicy.Policy{CORSOrigins: origins})))
	app.Get("/probe", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "1"}) })
	return app
}

func TestEdgeCORS_DisabledByDefault(t *testing.T) {
	app := corsApp(t, nil) // empty allowlist ⇒ no-op (ingress owns CORS)
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Origin", "https://hanzo.ai")
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("CORS default-off must emit no ACAO, got %q", got)
	}
	if res.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
}

func TestEdgeCORS_ReflectsAllowlistedOrigin(t *testing.T) {
	app := corsApp(t, []string{"*.hanzo.ai"})
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Origin", "https://console.hanzo.ai")
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "https://console.hanzo.ai" {
		t.Fatalf("ACAO = %q, want reflected origin", got)
	}
	if got := res.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("ACAC = %q, want true", got)
	}
	if got := res.Header.Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q, want Origin", got)
	}
	if res.StatusCode != 200 {
		t.Fatalf("actual request must still reach the handler (200), got %d", res.StatusCode)
	}
}

func TestEdgeCORS_PreflightShortCircuits(t *testing.T) {
	reached := false
	app := zip.New(zip.Config{})
	app.Use(EdgeCORS(staticPol(t, gatewaypolicy.Policy{CORSOrigins: []string{"*.hanzo.ai"}})))
	app.Use(func(c *zip.Ctx) error { reached = true; return c.Continue() })
	app.Get("/probe", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "1"}) })

	req := httptest.NewRequest(http.MethodOptions, "/probe", nil)
	req.Header.Set("Origin", "https://hanzo.ai")
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.StatusCode != 204 {
		t.Fatalf("preflight status = %d, want 204", res.StatusCode)
	}
	if reached {
		t.Fatal("preflight must short-circuit BEFORE the rest of the chain (no auth work)")
	}
	if got := res.Header.Get("Access-Control-Allow-Methods"); got != corsAllowMethods {
		t.Fatalf("ACA-Methods = %q, want %q", got, corsAllowMethods)
	}
	if got := res.Header.Get("Access-Control-Allow-Headers"); got != corsAllowHeaders {
		t.Fatalf("ACA-Headers = %q", got)
	}
	if got := res.Header.Get("Access-Control-Max-Age"); got != corsMaxAge {
		t.Fatalf("ACA-Max-Age = %q, want %q", got, corsMaxAge)
	}
}

func TestEdgeCORS_UnknownOriginGetsNothing(t *testing.T) {
	app := corsApp(t, []string{"*.hanzo.ai"})
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Origin", "https://evil.example")
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("unknown origin must receive no ACAO, got %q", got)
	}
}

// ── EdgeRateLimit ────────────────────────────────────────────────────────────

// rateApp mounts EdgeRateLimit with a small per-IP limit and a wide window so the
// window never rolls over mid-test.
func edgeRateApp(t *testing.T, limit int, enabled bool) *zip.App {
	perIP := limit
	if !enabled {
		perIP = 0 // disabled ⇒ no cap
	}
	app := zip.New(zip.Config{})
	app.Use(EdgeRateLimit(staticPol(t, gatewaypolicy.Policy{PerIPRPM: perIP, WindowSec: 60})))
	app.Get("/probe", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "1"}) })
	return app
}

func edgeGet(t *testing.T, app *zip.App, xff string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	return res.StatusCode
}

func TestEdgeRateLimit_PerIP(t *testing.T) {
	app := edgeRateApp(t, 3, true)
	// 3 allowed, 4th over the limit for the same public IP.
	for i := 1; i <= 3; i++ {
		if code := edgeGet(t, app, "203.0.113.7"); code != 200 {
			t.Fatalf("request %d: status %d, want 200", i, code)
		}
	}
	if code := edgeGet(t, app, "203.0.113.7"); code != 429 {
		t.Fatalf("4th request must be 429, got %d", code)
	}
	// A DIFFERENT client IP is on its own bucket, unaffected.
	if code := edgeGet(t, app, "198.51.100.9"); code != 200 {
		t.Fatalf("distinct IP must not share a bucket, got %d", code)
	}
}

func TestEdgeRateLimit_XFFChainUsesLeftmost(t *testing.T) {
	app := edgeRateApp(t, 2, true)
	// The real client is the leftmost XFF entry; proxy hops after it must not
	// change the bucket identity.
	for i := 1; i <= 2; i++ {
		if code := edgeGet(t, app, "203.0.113.50, 10.0.0.1"); code != 200 {
			t.Fatalf("request %d: status %d, want 200", i, code)
		}
	}
	if code := edgeGet(t, app, "203.0.113.50, 10.0.0.2"); code != 429 {
		t.Fatalf("same leftmost client must share the bucket → 429, got %d", code)
	}
}

func TestEdgeRateLimit_InClusterExempt(t *testing.T) {
	app := edgeRateApp(t, 2, true)
	// No X-Forwarded-For ⇒ in-cluster direct caller ⇒ never limited.
	for i := 1; i <= 5; i++ {
		if code := edgeGet(t, app, ""); code != 200 {
			t.Fatalf("in-cluster request %d must be exempt, got %d", i, code)
		}
	}
}

func TestEdgeRateLimit_Disabled(t *testing.T) {
	app := edgeRateApp(t, 2, false)
	for i := 1; i <= 5; i++ {
		if code := edgeGet(t, app, "203.0.113.7"); code != 200 {
			t.Fatalf("disabled limiter must pass request %d, got %d", i, code)
		}
	}
}

func TestEdgeRateLimit_SweepEvictsExpired(t *testing.T) {
	now := time.Now()
	rl := &edgeIPLimiter{buckets: map[string]*edgeBucket{}}
	// Both windows already closed; a sweep (>= window since the zero lastSweep)
	// must drain the map so per-IP cardinality never accumulates unbounded.
	rl.buckets["a"] = &edgeBucket{count: 1, reset: now.Add(-2 * time.Second)}
	rl.buckets["b"] = &edgeBucket{count: 1, reset: now.Add(-time.Second)}
	rl.sweepLocked(now, time.Second)
	if len(rl.buckets) != 0 {
		t.Fatalf("expired buckets must be evicted, %d remain", len(rl.buckets))
	}
}
