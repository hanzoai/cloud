package apps

import (
	"io"
	"net/http/httptest"
	"testing"

	commercemid "github.com/hanzoai/commerce/middleware"
	"github.com/zap-proto/zip"
)

// TestCommerceErrorScope proves commerceErrorScope() confines commerce's always-500
// JSON envelope to commerce-owned prefixes: a post-commerce subsystem route
// (/v1/projects) that returns a typed 403 renders 403 (zip default), while a
// commerce route (/v1/store/...) still gets commerce's envelope. Mirrors the frozen
// order: kms (before commerce) → commerce /v1 group chain → projects + store (after).
// Regression for the release-smoke failure where 14 post-commerce endpoints 500'd.
func TestCommerceErrorScope(t *testing.T) {
	app := zip.New(zip.Config{})

	// kms (before commerce) — a clean 403 baseline (never wrapped by commerce).
	app.Get("/v1/kms/health", func(c *zip.Ctx) error { return zip.ErrForbidden("kms says no") })

	// commerce (position 39): the REAL group chain, now with the scoped envelope.
	sv1 := app.Group("/v1")
	sv1.Use(commercemid.AddHost(), commercemid.RequestContext(), commerceErrorScope())

	// projects (after commerce) — typed 403; must NOT be clobbered to 500.
	app.Get("/v1/projects", func(c *zip.Ctx) error { return zip.ErrForbidden("X-Org-Id required") })
	// a commerce store route (after commerce) — typed 403; commerce envelope applies.
	app.Get("/v1/store/current", func(c *zip.Ctx) error { return zip.ErrForbidden("store needs org") })

	probe := func(path string) (int, string) {
		req := httptest.NewRequest("GET", path, nil)
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	for _, tc := range []struct {
		path     string
		wantCode int
	}{
		{"/v1/kms/health", 403},
		{"/v1/projects", 403},      // was 500 before the scope — the release-smoke failure
		{"/v1/store/current", 500}, // commerce envelope still owns its route (always-500 by design)
	} {
		code, body := probe(tc.path)
		t.Logf("%-20s -> %d  %s", tc.path, code, body)
		if code != tc.wantCode {
			t.Errorf("%s: got %d, want %d (%s)", tc.path, code, tc.wantCode, body)
		}
	}

	for _, p := range []struct {
		path string
		own  bool
	}{
		{"/v1/store/current", true},
		{"/v1/commerce/checkout", true},
		{"/v1/projects", false},
		{"/v1/agent/presets", false},
		{"/v1/agents", false},
	} {
		if got := hasCommercePrefix(p.path); got != p.own {
			t.Errorf("hasCommercePrefix(%q) = %v, want %v", p.path, got, p.own)
		}
	}
}
