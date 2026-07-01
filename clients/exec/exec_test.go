package exec

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fiber "github.com/gofiber/fiber/v3"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/zip"
	luxlog "github.com/luxfi/log"
)

// Mount() must register the overlapping static + wildcard routes (/v1/exec and
// /v1/exec/*) on a real Fiber router WITHOUT panicking, and a request routed
// through the whole app must reach the guarded proxy. This catches
// route-registration errors the direct-handler tests can't.
func TestMountRoutesThroughRouter(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"session_id":"s","stdout":"ok\n","stderr":"","files":[]}`)
	}))
	defer up.Close()
	t.Setenv("CODE_EXEC_UPSTREAM", up.URL)
	t.Setenv("CODE_EXEC_API_KEY", "k")

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	fa := app.Fiber()

	// Exact prefix, a wildcard subpath, and a file path all route to the proxy.
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/exec"},
		{http.MethodPost, "/v1/exec/programmatic"},
		{http.MethodGet, "/v1/files/sess-1"},
	} {
		req := httptest.NewRequest(tc.method, "http://api.hanzo.ai"+tc.path,
			strings.NewReader(`{"lang":"py","code":"x=1"}`))
		req.Header.Set("X-API-Key", "k")
		req.Header.Set("Content-Type", "application/json")
		resp, err := fa.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s %s: status %d, want 200 (routed to proxy)", tc.method, tc.path, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}

	// And the guard still fires through the router: wrong key ⇒ 401.
	req := httptest.NewRequest(http.MethodPost, "http://api.hanzo.ai/v1/exec", nil)
	req.Header.Set("X-API-Key", "wrong")
	resp, err := fa.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("guarded route: %v", err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-key through router: status %d, want 401", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

// Mount validates its inputs.
func TestMountRejectsBadInputs(t *testing.T) {
	if err := Mount(nil, cloud.Deps{Logger: luxlog.New("test")}); err == nil {
		t.Fatal("Mount(nil app) should error")
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{}); err == nil {
		t.Fatal("Mount(nil logger) should error")
	}
}

// The proxy must forward the request path + body verbatim to the sandboxed
// executor and return its response unchanged — the behavior that makes cloud a
// transparent edge in front of the sandbox, with no contract drift.
func TestProxyForwardsVerbatim(t *testing.T) {
	var gotPath, gotHost, gotBody, gotKey string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotHost = r.Host
		gotKey = r.Header.Get("X-API-Key")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"session_id":"s1","stdout":"hi\n","stderr":"","files":[]}`)
	}))
	defer up.Close()

	proxy, err := newProxy(up.URL)
	if err != nil {
		t.Fatalf("newProxy: %v", err)
	}
	t.Setenv("CODE_EXEC_API_KEY", "secret-key")
	h := guard(proxy)

	req := httptest.NewRequest(http.MethodPost, "http://api.hanzo.ai/v1/exec",
		strings.NewReader(`{"lang":"py","code":"print('hi')"}`))
	req.Header.Set("X-API-Key", "secret-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if gotPath != "/v1/exec" {
		t.Fatalf("upstream path = %q, want /v1/exec (verbatim, no rewrite)", gotPath)
	}
	if gotBody != `{"lang":"py","code":"print('hi')"}` {
		t.Fatalf("upstream body = %q, want the request body verbatim", gotBody)
	}
	if gotKey != "secret-key" {
		t.Fatalf("upstream X-API-Key = %q, want it forwarded", gotKey)
	}
	if gotHost == "api.hanzo.ai" {
		t.Fatalf("upstream Host = %q, want the executor vhost (not the edge host)", gotHost)
	}
	if !strings.Contains(rec.Body.String(), `"stdout":"hi\n"`) {
		t.Fatalf("response not passed through: %s", rec.Body.String())
	}
}

// A programmatic-tool-calling subpath (/v1/exec/programmatic) and file paths
// must also be forwarded verbatim.
func TestProxyForwardsSubpaths(t *testing.T) {
	var gotPath string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `[]`)
	}))
	defer up.Close()
	proxy, err := newProxy(up.URL)
	if err != nil {
		t.Fatalf("newProxy: %v", err)
	}
	t.Setenv("CODE_EXEC_API_KEY", "k")
	h := guard(proxy)

	for _, p := range []string{"/v1/exec/programmatic", "/v1/files/sess-123", "/v1/download/abc"} {
		req := httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai"+p, nil)
		req.Header.Set("X-API-Key", "k")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", p, rec.Code)
		}
		if gotPath != p {
			t.Fatalf("%s: upstream path = %q, want verbatim", p, gotPath)
		}
	}
}

// Fail closed: with no configured key the surface returns 503, never proxies.
func TestGuardUnsetKeyFailsClosed(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream must NOT be reached when key is unset")
	}))
	defer up.Close()
	proxy, _ := newProxy(up.URL)
	t.Setenv("CODE_EXEC_API_KEY", "")
	h := guard(proxy)

	req := httptest.NewRequest(http.MethodPost, "http://api.hanzo.ai/v1/exec", nil)
	req.Header.Set("X-API-Key", "anything")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (fail closed on unset key)", rec.Code)
	}
}

// Wrong key ⇒ 401, upstream never reached.
func TestGuardWrongKeyRejected(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream must NOT be reached with a wrong key")
	}))
	defer up.Close()
	proxy, _ := newProxy(up.URL)
	t.Setenv("CODE_EXEC_API_KEY", "right")
	h := guard(proxy)

	req := httptest.NewRequest(http.MethodPost, "http://api.hanzo.ai/v1/exec", nil)
	req.Header.Set("X-API-Key", "wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (wrong key)", rec.Code)
	}
}

func TestNewProxyRejectsBadURL(t *testing.T) {
	if _, err := newProxy("://nope"); err == nil {
		t.Fatal("expected error for malformed upstream URL")
	}
	if _, err := newProxy("/relative/only"); err == nil {
		t.Fatal("expected error for non-absolute upstream URL")
	}
}

func TestUpstreamDefaultAndOverride(t *testing.T) {
	t.Setenv("CODE_EXEC_UPSTREAM", "")
	if got := upstream(); got != defaultUpstream {
		t.Fatalf("upstream() = %q, want default %q", got, defaultUpstream)
	}
	t.Setenv("CODE_EXEC_UPSTREAM", "http://sandbox:8000")
	if got := upstream(); got != "http://sandbox:8000" {
		t.Fatalf("upstream() = %q, want override", got)
	}
}
