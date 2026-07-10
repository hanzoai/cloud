package tracker

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// doScoped runs a JSON request carrying a validated principal (X-Org-Id +
// X-User-Id) and an optional X-Project-Id sub-scope — the header tracker keys
// its per-project SQLite file on.
func doScoped(t *testing.T, app *zip.App, method, path, org, project string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
		req.Header.Set("X-User-Id", "u_"+org) // validated principal (org() gates on it)
	}
	if project != "" {
		req.Header.Set("X-Project-Id", project)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// TestPerProjectStoreFileIsolation proves tracker is PROJECT-scoped: two IAM
// projects under ONE org resolve to two nested SQLite files, and a tracker
// project created under one IAM project is invisible under another (no
// cross-project read) — the physical project boundary.
func TestPerProjectStoreFileIsolation(t *testing.T) {
	dir := t.TempDir()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: dir}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })

	// Under IAM project "alpha", org acme creates tracker project ENG.
	if code, b := doScoped(t, app, http.MethodPost, "/v1/tracker/projects", "acme", "alpha",
		map[string]any{"key": "ENG", "name": "Engineering"}); code != http.StatusCreated {
		t.Fatalf("create under alpha: %d %s", code, b)
	}

	// Under IAM project "beta", the SAME org sees NO projects — physical isolation.
	code, b := doScoped(t, app, http.MethodGet, "/v1/tracker/projects", "acme", "beta", nil)
	if code != http.StatusOK {
		t.Fatalf("list under beta: %d %s", code, b)
	}
	var betaList []projectView
	if err := json.Unmarshal(b, &betaList); err != nil {
		t.Fatalf("beta list json: %v (%s)", err, b)
	}
	if len(betaList) != 0 {
		t.Fatalf("IAM project beta saw alpha's tracker project: %+v", betaList)
	}

	// Under "alpha" the project is visible.
	code, b = doScoped(t, app, http.MethodGet, "/v1/tracker/projects", "acme", "alpha", nil)
	if code != http.StatusOK {
		t.Fatalf("list under alpha: %d %s", code, b)
	}
	var alphaList []projectView
	if err := json.Unmarshal(b, &alphaList); err != nil {
		t.Fatalf("alpha list json: %v (%s)", err, b)
	}
	if len(alphaList) != 1 || alphaList[0].Key != "ENG" {
		t.Fatalf("alpha should see exactly ENG, got %+v", alphaList)
	}

	// Two nested per-project SQLite files exist.
	fAlpha := filepath.Join(dir, "orgs", "acme", "projects", "alpha", "tracker.db")
	fBeta := filepath.Join(dir, "orgs", "acme", "projects", "beta", "tracker.db")
	for _, p := range []string{fAlpha, fBeta} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected nested per-project tracker.db at %s: %v", p, err)
		}
	}
}
