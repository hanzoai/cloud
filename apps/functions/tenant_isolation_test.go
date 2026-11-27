package functions

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"

	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	"github.com/hanzoai/cloud/internal/sqltest"
	_ "github.com/hanzoai/cloud/sdk/kit/master"
	"github.com/hanzoai/cloud/sdk/locus"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestPerOrgStoreFileIsolation proves the conversion from ONE shared
// functions.db to a per-org file: two orgs get two physical DBs and neither can
// list the other's functions.
func TestPerOrgStoreFileIsolation(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		dir := t.TempDir()
		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		t.Setenv("CLOUD_DATA_DIR", dir)
		if err := Use(app, cloud.Deps{}); err != nil {
			t.Fatalf("Use:  %v", err)
		}
		t.Cleanup(func() { _ = Shutdown() })

		if code, b := do(t, app, http.MethodPost, "/v1/function", "orga",
			map[string]any{"name": "resize", "runtime": "python", "code": "print(1)"}); code != http.StatusCreated {
			t.Fatalf("orgA create: %d %s", code, b)
		}

		code, b := do(t, app, http.MethodGet, "/v1/function", "orgb", nil)
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

		if code, b := do(t, app, http.MethodPost, "/v1/function", "orgb",
			map[string]any{"name": "resize", "runtime": "python", "code": "print(2)"}); code != http.StatusCreated {
			t.Fatalf("orgB create (same name): %d %s", code, b)
		}

		// Close before stat: on the pure-Go codec the database is written back at
		// CLOSE, so an open store has not yet landed its file. Shutdown is idempotent,
		// so the Cleanup above still runs harmlessly.
		if err := Shutdown(); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}

		// Two physically distinct per-org DBs exist: files on SQLite, schemas on hanzo-sql.
		for _, org := range []string{"orga", "orgb"} {
			if locus.DSN() != "" {
				ns, err := principal.OrgNamespace(org, "")
				if err != nil {
					t.Fatal(err)
				}
				if ok, err := locus.Exists(ns, "functions"); err != nil || !ok {
					t.Fatalf("expected per-org functions schema for %s: exists=%v err=%v", org, ok, err)
				}
				continue
			}
			p := filepath.Join(dir, "orgs", org, "functions.db")
			if _, err := os.Stat(p); err != nil {
				t.Fatalf("expected per-org functions store at %s: %v", p, err)
			}
		}
	})
}
