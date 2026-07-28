package git

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRootSmartHTTP_HostGuard verifies the root-level smart-HTTP routes
// (/:org/:repo/*) serve ONLY on the git host and fall through on every other
// host, so `git clone https://git.hanzo.ai/<org>/<repo>.git` works while a bare
// /:org/:repo can never shadow the api/console surfaces.
func TestRootSmartHTTP_HostGuard(t *testing.T) {
	app := mountApp(t) // Domain api.hanzo.test → gitHost git.hanzo.test

	// On the git host the root route reaches infoRefs; the PUSH advertisement
	// rejects a missing X-Org-Id with 403 (anonymous read exists only for
	// upload-pack on public repos) — proving the route matched and the handler
	// ran (not a routing miss, which would be 404).
	req := httptest.NewRequest(http.MethodGet, "/acme/repo.git/info/refs?service=git-receive-pack", nil)
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
	req = httptest.NewRequest(http.MethodGet, "/acme/repo.git/info/refs?service=git-receive-pack", nil)
	req.Host = "api.hanzo.test"
	resp, err = app.Fiber().Test(req, testCfg)
	if err != nil {
		t.Fatalf("api-host request: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("api host: want 404 (fell through), got %d", resp.StatusCode)
	}
}

// TestRootUI_HostGuard verifies the root-level UI routes ("/" and the GitHub-style
// "/:org/:repo" browse paths) serve native git's ui.go ONLY on the git host and
// fall through on every other host — so git.hanzo.ai/<org>/<repo> renders the
// native browser while a bare /:org/:repo never shadows the console SPA elsewhere.
func TestRootUI_HostGuard(t *testing.T) {
	app := mountApp(t) // Domain api.hanzo.test → gitHost git.hanzo.test

	// On the git host, GET / reaches uiHome; with no validated principal it now
	// renders the PUBLIC explore/landing (200) instead of 403 — OSS-first, no sign-in
	// to browse. The git-native body ("Hanzo Git") proves the route matched native
	// git and did not fall through to the console SPA.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Host = "git.hanzo.test"
	resp, err := app.Fiber().Test(req, testCfg)
	if err != nil {
		t.Fatalf("git-host / request: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("git host /: want 200 (public explore), got %d", resp.StatusCode)
	}
	if body, _ := io.ReadAll(resp.Body); !strings.Contains(string(body), "Hanzo Git") {
		t.Fatalf("git host /: want native git explore body (\"Hanzo Git\"), got %.120q", body)
	}

	// The GitHub-style browse path reaches uiRepo on the git host. A MISSING repo
	// answers native git's 404 "no such repository" (uiRepoAccess ran) — distinct
	// from a routing miss by the git-native error body.
	req = httptest.NewRequest(http.MethodGet, "/acme/widget", nil)
	req.Host = "git.hanzo.test"
	resp, err = app.Fiber().Test(req, testCfg)
	if err != nil {
		t.Fatalf("git-host /:org/:repo request: %v", err)
	}
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("git host /:org/:repo (missing): want 404 (uiRepo ran), got %d", resp.StatusCode)
	}
	if body, _ := io.ReadAll(resp.Body); !strings.Contains(string(body), "no such repository") {
		t.Fatalf("git host /:org/:repo: want native git 404 body, got %.120q", body)
	}

	// On the api host the root UI routes fall through (c.Next()); nothing else is
	// mounted in this git-only test app, so both 404 — proving they never serve off
	// the git host (where the console SPA catch-all owns the root instead).
	for _, path := range []string{"/", "/acme/widget"} {
		req = httptest.NewRequest(http.MethodGet, path, nil)
		req.Host = "api.hanzo.test"
		resp, err = app.Fiber().Test(req, testCfg)
		if err != nil {
			t.Fatalf("api-host %s request: %v", path, err)
		}
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("api host %s: want 404 (fell through), got %d", path, resp.StatusCode)
		}
	}
}
