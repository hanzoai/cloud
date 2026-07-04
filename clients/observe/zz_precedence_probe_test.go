package observe

import (
	"net/http/httptest"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// zz_precedence_probe reproduces the PRODUCTION Fiber route stack for the
// /v1/o11y/* surface exactly as MountAll builds it: observe (order 44) registers
// its three EXACT GET routes FIRST, then hanzoai/o11y (order 70) registers the
// catch-all `All("/v1/o11y/*")` reverse-proxy wildcard. The wildcard here is a
// SENTINEL that marks any request that reached the UNSCOPED proxy instead of the
// org-scoped observe handler. A request that lands on the sentinel is a
// route-precedence bypass of tenant scoping (attack #4).
func buildPrecedenceApp(t *testing.T) *zip.App {
	t.Helper()
	store, err := openSettingsStore(t.TempDir() + "/observe.db")
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	s := &service{store: store, log: luxlog.New("test"), adminOrg: "admin", vmURL: "http://127.0.0.1:1", dnsSuffix: ".test.invalid"}

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	// order 44: observe's scoped GET handlers.
	app.Get("/v1/o11y/logs", s.getLogs)
	app.Get("/v1/o11y/metrics", s.getMetrics)
	app.Get("/v1/o11y/status", s.getStatus)
	// order 70: the hanzoai/o11y catch-all reverse proxy (SENTINEL: 599).
	app.All("/v1/o11y/*", func(c *zip.Ctx) error {
		return c.String(599, "REACHED-UNSCOPED-PROXY")
	})
	return app
}

func TestRoutePrecedence_ProxyFallThrough(t *testing.T) {
	app := buildPrecedenceApp(t)

	// A VALIDATED principal for org "attacker" (X-User-Id present).
	hdr := func(r *httptest.ResponseRecorder) {}
	_ = hdr

	cases := []struct {
		name, method, path string
	}{
		{"canonical GET", "GET", "/v1/o11y/logs?product=kms"},
		{"trailing slash", "GET", "/v1/o11y/logs/?product=kms"},
		{"uppercase path", "GET", "/v1/o11y/LOGS?product=kms"},
		{"mixed case", "GET", "/v1/o11y/Logs?product=kms"},
		{"metrics canonical", "GET", "/v1/o11y/metrics?product=kms"},
		{"status canonical", "GET", "/v1/o11y/status?product=kms"},
		{"POST to logs path", "POST", "/v1/o11y/logs?product=kms"},
		{"HEAD to logs path", "HEAD", "/v1/o11y/logs?product=kms"},
		{"PUT to logs path", "PUT", "/v1/o11y/logs?product=kms"},
		{"DELETE to logs path", "DELETE", "/v1/o11y/logs?product=kms"},
		{"OPTIONS to logs path", "OPTIONS", "/v1/o11y/logs?product=kms"},
		{"PATCH to logs path", "PATCH", "/v1/o11y/logs?product=kms"},
		{"trailing slash + POST", "POST", "/v1/o11y/logs/?product=kms"},
		{"double slash", "GET", "//v1/o11y/logs?product=kms"},
		{"dot segment", "GET", "/v1/o11y/./logs?product=kms"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("X-Org-Id", "attacker")
		req.Header.Set("X-User-Id", "u_attacker")
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Logf("%-22s %-7s %-32s -> ERR %v", tc.name, tc.method, tc.path, err)
			continue
		}
		reached := "observe(scoped)"
		if resp.StatusCode == 599 {
			reached = "PROXY(UNSCOPED)"
		}
		t.Logf("%-22s %-7s %-34s -> HTTP %d  %s", tc.name, tc.method, tc.path, resp.StatusCode, reached)
		_ = resp.Body.Close()
	}
}
