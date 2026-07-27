package git

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/cek"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// mountAppDir mounts git on an explicit DataDir so a test can assert the
// per-org SQLite files land where the path convention says.
func mountAppDir(t *testing.T, dir string) *zip.App {
	t.Helper()
	t.Setenv("GIT_SSH_ADDR", "127.0.0.1:0") // ephemeral SSH port (no :2222 collisions)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: dir, Domain: "api.hanzo.test"}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// TestPerOrgStoreFileIsolation proves the conversion from ONE shared git.db to a
// per-org file: two orgs get two physical DBs, and one org can never list the
// other's repos (no cross-read at the store-file boundary).
func TestPerOrgStoreFileIsolation(t *testing.T) {
	dir := t.TempDir()
	app := mountAppDir(t, dir)

	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "orga",
		map[string]any{"name": "svc"}); code != http.StatusCreated {
		t.Fatalf("orgA create: %d %s", code, b)
	}

	// orgB sees an empty list — never orgA's repo.
	code, b := do(t, app, http.MethodGet, "/v1/git/repos", "orgb", nil)
	if code != http.StatusOK {
		t.Fatalf("orgB list: %d %s", code, b)
	}
	var listB struct {
		Data []repoView `json:"data"`
	}
	if err := json.Unmarshal(b, &listB); err != nil {
		t.Fatalf("orgB list json: %v (%s)", err, b)
	}
	if len(listB.Data) != 0 {
		t.Fatalf("orgB saw orgA's repos: %+v", listB.Data)
	}

	// orgB may create a same-named repo — its own file, no conflict.
	if code, b := do(t, app, http.MethodPost, "/v1/git/repos", "orgb",
		map[string]any{"name": "svc"}); code != http.StatusCreated {
		t.Fatalf("orgB create (same name): %d %s", code, b)
	}

	// Two physically distinct per-org DBs exist.
	fa := filepath.Join(dir, "orgs", "orga", "git.db")
	fb := filepath.Join(dir, "orgs", "orgb", "git.db")
	for _, p := range []string{fa, fb} {
		// cek.Exists, not os.Stat: a store still OPEN has not materialized its
		// database file on the pure-Go codec — only its sidecar is on disk.
		if !cek.Exists(p) {
			t.Fatalf("expected per-org git store at %s", p)
		}
	}
}
