package git

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRootSmartHTTP_HostGuard verifies the root-level smart-HTTP routes
// (/:org/:repo/*) serve ONLY on the git host and fall through on every other
// host, so `git clone https://git.hanzo.ai/<org>/<repo>.git` works while a bare
// /:org/:repo can never shadow the api/console surfaces.
func TestRootSmartHTTP_HostGuard(t *testing.T) {
	app := mountApp(t) // Domain api.hanzo.test → gitHost git.hanzo.test

	// On the git host the root route reaches infoRefs, whose first gate rejects
	// a missing X-Org-Id with 403 — proving the route matched and the handler
	// ran (not a routing miss).
	req := httptest.NewRequest(http.MethodGet, "/acme/repo.git/info/refs?service=git-upload-pack", nil)
	req.Host = "git.hanzo.test"
	resp, err := app.Fiber().Test(req, testCfg)
	if err != nil {
		t.Fatalf("git-host request: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("git host: want 403 (handler ran, X-Org-Id required), got %d", resp.StatusCode)
	}

	// On the api host the identical path has no /v1/git prefix, so the guard
	// falls through (c.Next()) and nothing matches → 404. Proves the root route
	// never serves off the git host.
	req = httptest.NewRequest(http.MethodGet, "/acme/repo.git/info/refs?service=git-upload-pack", nil)
	req.Host = "api.hanzo.test"
	resp, err = app.Fiber().Test(req, testCfg)
	if err != nil {
		t.Fatalf("api-host request: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("api host: want 404 (fell through), got %d", resp.StatusCode)
	}
}
