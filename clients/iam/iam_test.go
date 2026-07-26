package iam

import (
	"net/http"
	"net/http/httptest"
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
	for _, p := range iamPrefixes {
		have[p] = true
	}
	for _, n := range []string{"/v1/iam", "/login/oauth"} {
		if !have[n] {
			t.Errorf("iamPrefixes missing auth-critical prefix %q", n)
		}
	}
}

// TestMountFailClosed503 proves the fail-soft path: when the embed cannot boot,
// mountFailClosed serves an honest JSON 503 on every IAM prefix instead of letting
// /v1/iam/* fall through to the console SPA (HTML 200). cloud and every co-resident
// subsystem stay up — the blast-radius isolation the consolidation exists for.
func TestMountFailClosed503(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	mountFailClosed(app)
	for _, p := range []string{
		"/v1/iam/oauth/token",
		"/v1/iam/.well-known/jwks",
		"/login/oauth/authorize",
	} {
		resp, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, p, nil))
		if err != nil {
			t.Fatalf("Test(%s): %v", p, err)
		}
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s = %d, want 503 (fail-closed)", p, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
}

// TestPaths covers the store-path derivation: the SQLite file lands under {DataDir}/iam,
// and init_data.json resolves the standalone-iam default unless `initDataFile` overrides.
func TestPaths(t *testing.T) {
	dbPath, initData := paths(cloud.Deps{DataDir: "/var/data"})
	if dbPath != "/var/data/iam/iam2.db" {
		t.Errorf("dbPath = %q, want /var/data/iam/iam2.db", dbPath)
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
	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}
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
