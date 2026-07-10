package functions

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestPerOrgStoreFileIsolation proves the conversion from ONE shared
// functions.db to a per-org file: two orgs get two physical DBs and neither can
// list the other's functions.
func TestPerOrgStoreFileIsolation(t *testing.T) {
	dir := t.TempDir()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: dir}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })

	if code, b := do(t, app, http.MethodPost, "/v1/functions", "orga",
		map[string]any{"name": "resize", "runtime": "python", "code": "print(1)"}); code != http.StatusCreated {
		t.Fatalf("orgA create: %d %s", code, b)
	}

	code, b := do(t, app, http.MethodGet, "/v1/functions", "orgb", nil)
	if code != http.StatusOK {
		t.Fatalf("orgB list: %d %s", code, b)
	}
	var listB struct {
		Functions []functionView `json:"functions"`
	}
	if err := json.Unmarshal(b, &listB); err != nil {
		t.Fatalf("orgB list json: %v (%s)", err, b)
	}
	if len(listB.Functions) != 0 {
		t.Fatalf("orgB saw orgA's functions: %+v", listB.Functions)
	}

	if code, b := do(t, app, http.MethodPost, "/v1/functions", "orgb",
		map[string]any{"name": "resize", "runtime": "python", "code": "print(2)"}); code != http.StatusCreated {
		t.Fatalf("orgB create (same name): %d %s", code, b)
	}

	fa := filepath.Join(dir, "orgs", "orga", "functions.db")
	fb := filepath.Join(dir, "orgs", "orgb", "functions.db")
	for _, p := range []string{fa, fb} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected per-org functions.db at %s: %v", p, err)
		}
	}
}
