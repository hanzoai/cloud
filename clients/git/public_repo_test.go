package git

import (
	"crypto/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/cek"
)

// The cek data plane fail-closes without a master key on encryption-capable
// builds; supply one process-wide so the per-org git.db opens in local runs
// exactly as it does in CI (mirrors clients/featureflags).
func TestMain(m *testing.M) {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	cek.SetMasterKey(k)
	os.Exit(m.Run())
}

// TestPublicRepo_AnonymousRead proves the visibility model end to end:
//   - a repo defaults PRIVATE: anonymous fetch advertisement is a uniform 404
//   - PATCH {"public":true} (org-authed) flips it
//   - anonymous upload-pack advertisement then serves 200 with the git
//     advertisement content type — credential-less clone works (the build
//     fabric's requirement)
//   - anonymous receive-pack advertisement stays 403 — public is READ ONLY
//   - PATCH without auth is 403
func TestPublicRepo_AnonymousRead(t *testing.T) {
	app := mountApp(t)

	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "site"}); code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d", code)
	}

	anonGet := func(path string) *http.Response {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		resp, err := app.Fiber().Test(req, testCfg)
		if err != nil {
			t.Fatalf("anon %s: %v", path, err)
		}
		return resp
	}
	advPath := "/v1/git/acme/site.git/info/refs?service=git-upload-pack"

	// Private by default: anonymous read is a uniform not-found (no existence
	// oracle — a missing repo answers identically).
	if resp := anonGet(advPath); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("anon fetch on private repo: want 404, got %d", resp.StatusCode)
	}
	if resp := anonGet("/v1/git/acme/ghost.git/info/refs?service=git-upload-pack"); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("anon fetch on missing repo: want 404, got %d", resp.StatusCode)
	}

	// Unauthenticated PATCH may not flip visibility.
	if code, _ := do(t, app, http.MethodPatch, "/v1/git/repos/site", "", map[string]any{"public": true}); code != http.StatusForbidden {
		t.Fatalf("anon patch: want 403, got %d", code)
	}

	// Org-authed PATCH flips it and the view reflects it.
	code, body := do(t, app, http.MethodPatch, "/v1/git/repos/site", "acme", map[string]any{"public": true})
	if code != http.StatusOK {
		t.Fatalf("patch public: want 200, got %d (%s)", code, body)
	}
	if !strings.Contains(string(body), `"public":true`) {
		t.Fatalf("patch view missing public flag: %s", body)
	}

	// Anonymous fetch advertisement now serves.
	resp := anonGet(advPath)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("anon fetch on public repo: want 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-git-upload-pack-advertisement" {
		t.Fatalf("anon fetch content-type: got %q", ct)
	}

	// Push advertisement stays authenticated even on a public repo.
	if resp := anonGet("/v1/git/acme/site.git/info/refs?service=git-receive-pack"); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("anon push advert on public repo: want 403, got %d", resp.StatusCode)
	}

	// Flip back: anonymous read closes again.
	if code, _ := do(t, app, http.MethodPatch, "/v1/git/repos/site", "acme", map[string]any{"public": false}); code != http.StatusOK {
		t.Fatalf("patch private: want 200, got %d", code)
	}
	if resp := anonGet(advPath); resp.StatusCode != http.StatusNotFound {
		t.Fatalf("anon fetch after re-privating: want 404, got %d", resp.StatusCode)
	}
}

// TestPublicRepo_CreatePublic proves the create-time flag is honored, and that
// an authed caller of ANOTHER org still cannot address the repo through the
// path-vs-identity guard (public read is for the anonymous fetch path; an
// authenticated caller keeps strict org scoping).
func TestPublicRepo_CreatePublic(t *testing.T) {
	app := mountApp(t)

	code, body := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "oss", "public": true})
	if code != http.StatusCreated || !strings.Contains(string(body), `"public":true`) {
		t.Fatalf("create public: code=%d body=%s", code, body)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/git/acme/oss.git/info/refs?service=git-upload-pack", nil)
	resp, err := app.Fiber().Test(req, testCfg)
	if err != nil {
		t.Fatalf("anon fetch: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("anon fetch on create-time public repo: want 200, got %d", resp.StatusCode)
	}

	// A DIFFERENT authenticated org addressing acme's repo path is refused by
	// the path-vs-identity guard (unchanged by the public bit).
	req = httptest.NewRequest(http.MethodGet, "/v1/git/acme/oss.git/info/refs?service=git-upload-pack", nil)
	req.Header.Set("X-Org-Id", "rival")
	req.Header.Set("X-User-Id", "u_rival")
	resp, err = app.Fiber().Test(req, testCfg)
	if err != nil {
		t.Fatalf("cross-org fetch: %v", err)
	}
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-org fetch: want 403 (path-vs-identity), got %d", resp.StatusCode)
	}
}
