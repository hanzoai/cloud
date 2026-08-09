package iam

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/iam/pkg/model"
	"github.com/hanzoai/iam/pkg/store"
)

// TestPrefixesCoverAuthCritical guards that the fail-closed prefix set never loses an
// auth-critical surface hanzo.id serves — the operator SSO chain and every relying
// party depend on these exact prefixes being served in-process.
func TestPrefixesCoverAuthCritical(t *testing.T) {
	have := map[string]bool{}
	for _, p := range Prefixes {
		have[p] = true
	}
	for _, n := range []string{"/v1/iam", "/login/oauth"} {
		if !have[n] {
			t.Errorf("Prefixes missing auth-critical prefix %q", n)
		}
	}
}

// TestDegradedMountAnswers503: with no identity store, identity addresses answer
// an honest 503 rather than falling through to the console SPA. In a plugin binary
// the terminal handler is that SPA, so a missed address does not 404 — it answers
// 200 with HTML, and a relying party parses a web page as its discovery document.
// That is why /.well-known/openid-configuration is asserted here.
//
// Verified by probe: with a nil store the full route table is still registered and
// its handlers refuse, so the published document does not depend on whether a
// volume mounted. DataDir points at a FILE so the store beneath cannot open.
func TestDegradedMountAnswers503(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(notADir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: notADir}); err != nil {
		t.Fatalf("Mount must stay up with no store, cloud depends on it: %v", err)
	}
	if DB() != nil {
		t.Error("DB() must be nil with no store so in-process readers can tell")
	}

	// Method matters: these are the real verbs. A GET against a POST-only address
	// answers 405, which is neither the refusal nor the SPA and would prove nothing.
	for _, c := range []struct {
		method, path string
	}{
		{http.MethodPost, "/v1/iam/oauth/token"},
		{http.MethodGet, "/v1/iam/.well-known/jwks"},
		{http.MethodGet, "/.well-known/openid-configuration"},
		{http.MethodGet, "/.well-known/oauth-authorization-server"},
	} {
		resp, err := app.Test(httptest.NewRequest(c.method, c.path, nil))
		if err != nil {
			t.Fatalf("Test(%s %s): %v", c.method, c.path, err)
		}
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("degraded %s %s = %d, want 503 (fail-closed, not the console SPA)", c.method, c.path, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
}

// TestPaths covers the store-location derivation: the store opens under DataDir (its
// place within that is namespace's, not this package's), and init_data.json resolves
// the standalone-iam default unless `initDataFile` overrides.
func TestPaths(t *testing.T) {
	dir, initData := paths(cloud.Deps{DataDir: "/var/data"})
	if dir != "/var/data" {
		t.Errorf("dir = %q, want /var/data", dir)
	}
	if initData != "init_data.json" {
		t.Errorf("initData = %q, want the CWD-relative default", initData)
	}
	t.Setenv("initDataFile", "/etc/iam/init_data.json")
	if _, initData := paths(cloud.Deps{DataDir: "/var/data"}); initData != "/etc/iam/init_data.json" {
		t.Errorf("initData override not honored, got %q", initData)
	}
}

// TestDBLifecycleAndStore is the end-to-end contract for the in-process store accessor:
// DB() is nil until Mount runs (the nil-guard contract sibling subsystems rely on), and
// after a successful Mount DB() returns the live orm.DB that pkg/store reads/writes the
// SAME project rows through — the whole reason Layer 3 (clients/platform, clients/deploy)
// can drop iam-v1's in-process object store.
func TestDBLifecycleAndStore(t *testing.T) {
	embeddedDB = nil // assert the pre-Mount nil-guard contract from a known state
	if DB() != nil {
		t.Fatal("DB() must be nil before Mount")
	}

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: dataDirWithStore(t)}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	if DB() == nil {
		t.Fatal("DB() must be non-nil after a successful Mount")
	}

	// The embedded store is the ONE project store: write via pkg/store over DB(), read it
	// back by its owner/name id, and confirm tenant-scoped listing sees exactly it.
	ok, err := store.AddProject(DB(), &model.Project{Owner: "hanzo", Name: "alpha", DisplayName: "Alpha"})
	if err != nil || !ok {
		t.Fatalf("AddProject over DB(): ok=%v err=%v", ok, err)
	}
	got, err := store.GetProject(DB(), "hanzo/alpha")
	if err != nil || got == nil || got.Name != "alpha" {
		t.Fatalf("GetProject over DB(): got=%+v err=%v", got, err)
	}
	rows, err := store.GetOrganizationProjects(DB(), "hanzo")
	if err != nil || len(rows) != 1 {
		t.Fatalf("GetOrganizationProjects over DB(): rows=%d err=%v", len(rows), err)
	}
}
