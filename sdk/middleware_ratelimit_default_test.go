package cloud

// The static rate floor, asserted where it bites.
//
// The property under test is the one that was missing: an org nobody configured
// is still bounded on a service class that names a default. "No rule configured"
// used to mean "no limit", which on an unpriced service means a free denial of
// service — nothing downstream ever says stop.

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// floorReq sends one request as a validated principal of org, the identity
// ScopeRateLimit keys on (the gateway mints these headers).
func floorReq(t *testing.T, app *zip.App, org, path string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, nil)
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "alice")
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test request: %v", err)
	}
	return resp
}
