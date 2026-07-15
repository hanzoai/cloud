package iam

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/beego/v2/server/web"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestPrefixesCoverAuthCritical guards that the mount set never loses an
// auth-critical surface hanzo.id serves — the operator SSO chain and every
// relying party depend on these exact prefixes being served in-process.
func TestPrefixesCoverAuthCritical(t *testing.T) {
	have := map[string]bool{}
	for _, p := range iamPrefixes {
		have[p] = true
	}
	for _, n := range []string{"/v1/iam", "/.well-known", "/login/oauth"} {
		if !have[n] {
			t.Errorf("iamPrefixes missing auth-critical prefix %q", n)
		}
	}
}

// TestMountHandlerPreservesFullPath verifies the routing plumbing the embedded
// IAM handler relies on: zip.App.Mount(prefix, h) must dispatch prefix/* to h
// with the ORIGINAL request path intact. Beego routes on the full path
// (/v1/iam/oauth/token, not a stripped /oauth/token), so a prefix-stripping mount
// would 404 every OAuth/OIDC call. This drives the exact mountHandler call Mount
// uses, with a stub handler, so it runs without the full Beego runtime.
func TestMountHandlerPreservesFullPath(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})

	var gotPath string
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	mountHandler(app, stub)

	for _, want := range []string{
		"/v1/iam/oauth/token",
		"/v1/iam/oauth/authorize",
		"/v1/iam/.well-known/jwks",
		"/.well-known/openid-configuration",
		"/.well-known/jwks",
		"/login/oauth/authorize",
	} {
		gotPath = ""
		resp, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, want, nil))
		if err != nil {
			t.Fatalf("Test(%s): %v", want, err)
		}
		_ = resp.Body.Close()
		if gotPath != want {
			t.Errorf("embedded handler saw path %q, want full %q (prefix stripped?)", gotPath, want)
		}
	}
}

// TestMountFailClosed503 proves the fail-soft path: when the embed cannot boot,
// mountFailClosed serves an honest JSON 503 on every IAM prefix instead of
// letting /v1/iam/* fall through to the console SPA (HTML 200). cloud and every
// co-resident subsystem stay up — the blast-radius isolation the consolidation
// exists for.
func TestMountFailClosed503(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	mountFailClosed(app)
	for _, p := range []string{
		"/v1/iam/oauth/token",
		"/v1/iam/.well-known/jwks",
		"/.well-known/openid-configuration",
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

// TestIsolateDatabase proves the co-residence unblock: isolateDatabase pins IAM's
// SQLite handle to its OWN iam.db under the IAM data dir via IAM_DATABASE_URL —
// the IAM-scoped key that conf.GetConfigDataSourceName honors ABOVE the shared
// `dataSourceName` a deployment sets for the sibling `ai` fork. Without this the
// embedded IAM opens ai's database and the binary crashes at boot (SQLITE_CANTOPEN
// / casdoor-table auto-migration into ai's store). It must (1) set an iam-owned
// DSN, (2) NOT collide with ai's dataSourceName, and (3) respect an operator
// override.
func TestIsolateDatabase(t *testing.T) {
	const dataDir = "/data/iam"
	const aiDSN = "file:/data/ai.db?cache=shared" // what a deployment sets for `ai`

	// (1)+(2): default fills an iam-owned DSN that is NOT ai's.
	t.Setenv("dataSourceName", aiDSN)
	os.Unsetenv("IAM_DATABASE_URL")
	isolateDatabase(dataDir)
	got := os.Getenv("IAM_DATABASE_URL")
	if got == "" {
		t.Fatal("IAM_DATABASE_URL unset after isolateDatabase — IAM would resolve ai's dataSourceName")
	}
	if got == aiDSN {
		t.Fatalf("IAM_DATABASE_URL == ai's dataSourceName (%q) — the collision is NOT isolated", got)
	}
	if !strings.Contains(got, "/data/iam/iam.db") {
		t.Errorf("IAM_DATABASE_URL = %q, want IAM's own iam.db under the data dir", got)
	}

	// (3): an operator-set IAM_DATABASE_URL (e.g. an external DSN) is respected.
	const override = "file:/mnt/custom/iam.db?cache=shared"
	t.Setenv("IAM_DATABASE_URL", override)
	isolateDatabase(dataDir)
	if os.Getenv("IAM_DATABASE_URL") != override {
		t.Errorf("isolateDatabase clobbered operator override: got %q, want %q", os.Getenv("IAM_DATABASE_URL"), override)
	}
}

// TestInitSessionsIdempotent proves the session-manager hook is safe to call more
// than once (Beego's GlobalSessions is a process singleton) and wires the memory
// provider IAM configures. Beego defaults SessionProvider to "memory" and
// auto-registers it, so this runs without iamserver.Init.
func TestInitSessionsIdempotent(t *testing.T) {
	if web.GlobalSessions == nil {
		if err := initSessions(); err != nil {
			t.Fatalf("initSessions: %v", err)
		}
	}
	if web.GlobalSessions == nil {
		t.Fatal("GlobalSessions still nil after initSessions")
	}
	if err := initSessions(); err != nil {
		t.Fatalf("initSessions second call (must be a no-op): %v", err)
	}
}
