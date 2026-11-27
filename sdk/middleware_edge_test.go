package cloud

// Tests for the edge policy middleware (middleware_edge.go): browser CORS and the
// per-client-IP flood cap. They drive real requests through the zip/fiber stack,
// so the whole path runs end-to-end (header set, preflight short-circuit, 429).

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/zap-proto/zip"
)

// staticPol builds a static-only (no SQLite) policy store for the middleware tests.
func staticPol(t *testing.T, p edge.Policy) *edge.Store {
	t.Helper()
	s, _ := edge.New("", "admin", p) // "" dataDir ⇒ static-only, no file.
	return s
}

func edgeGet(t *testing.T, app *zip.App, xff string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	return res.StatusCode
}
