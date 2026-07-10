package iam

import (
	"net/http"
	"net/http/httptest"
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
