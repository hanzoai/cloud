package git

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	_ "github.com/hanzoai/cloud/sdk/kit/master"
)

// TestPathOrgReadCreatesNothing proves an anonymous read naming an org that has
// no store answers 404 and leaves no directory behind, for the browser view and
// the smart-HTTP advertisement alike, while a real public repo still serves.
func TestPathOrgReadCreatesNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dir := t.TempDir()
		app := mountAppDir(t, dir)

		if code, _ := do(t, app, http.MethodPost, "/v1/git/repos", "acme", map[string]any{"name": "site"}); code != http.StatusCreated {
			t.Fatalf("create: want 201, got %d", code)
		}
		if code, body := do(t, app, http.MethodPatch, "/v1/git/repos/site", "acme", map[string]any{"public": true}); code != http.StatusOK {
			t.Fatalf("patch public: want 200, got %d (%s)", code, body)
		}

		get := func(path string) int {
			resp, err := app.Test(httptest.NewRequest(http.MethodGet, path, nil), testCfg)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			return resp.StatusCode
		}
		before := orgDirs(t, dir)

		for _, path := range []string{
			"/v1/git/Hanzo-Example-Folio-Creative-Agency-Template/site",
			"/v1/git/0d9f98ce-e3ef-40ec-866a-30d0d95d1a2b/site",
			"/v1/git/ghost/site",
			"/v1/git/ghost/site/tree/src",
			"/v1/git/ghost/site.git/info/refs?service=git-upload-pack",
		} {
			if code := get(path); code != http.StatusNotFound {
				t.Fatalf("anon %s: want 404, got %d", path, code)
			}
		}
		if after := orgDirs(t, dir); len(after) != len(before) {
			t.Fatalf("anonymous reads created org directories: before %v, after %v", before, after)
		}

		if code := get("/v1/git/acme/site.git/info/refs?service=git-upload-pack"); code != http.StatusOK {
			t.Fatalf("anon fetch on public repo: want 200, got %d", code)
		}
	})
}

// orgDirs lists every directory under the data dir, wherever this build lays
// the per-org stores out.
func orgDirs(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}
