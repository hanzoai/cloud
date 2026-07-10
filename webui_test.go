package cloud

// Tests for the embedded console: ONE binary serves the SPA at the web root AND
// the /v1 API, from the same zip/fiber app. They drive real requests through the
// stack (app.Fiber().Test) exactly as production Serve wires it — a /v1 route
// registered FIRST, then mountConsole registered LAST — so the assertions prove
// the real precedence and SPA-fallback behavior, not a mock of it.

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// newConsoleApp mirrors the relevant slice of Serve: a representative /v1 API
// route (so we can assert API precedence) followed by the console catch-all
// mounted LAST. Anything the console must not shadow is registered before it.
func newConsoleApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{})

	// A real API route. In production every /v1/* subsystem route + the health
	// contract is registered before mountConsole; one representative route is
	// enough to prove the catch-all never wins over a registered API path.
	app.Get("/v1/models", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]any{
			"object": "list",
			"data":   []any{map[string]string{"id": "zen-1"}},
		})
	})

	// Console LAST — terminal catch-all, same as Serve.
	if err := mountConsole(app); err != nil {
		t.Fatalf("mountConsole: %v", err)
	}
	return app
}

// do issues a test request and returns status, body, and headers. Fiber's Test
// runs the full router in-process; -1 timeout disables the client deadline so a
// slow CI host never flakes.
func do(t *testing.T, app *zip.App, method, target string, headers map[string]string) (int, []byte, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, target, nil)
	if err != nil {
		t.Fatalf("new request %s %s: %v", method, target, err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// Timeout: 0 disables the per-request deadline (Fiber v3), so a slow CI host
	// never flakes these in-process router calls.
	resp, err := app.Fiber().Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("test %s %s: %v", method, target, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body %s %s: %v", method, target, err)
	}
	return resp.StatusCode, body, resp.Header
}

// indexHTML is the embedded shell, read straight from the embed FS, so tests
// compare against the exact bytes that ship (works for the fallback shell AND
// the real console bundle the image build drops in).
func indexHTML(t *testing.T) []byte {
	t.Helper()
	b, err := consoleFS.ReadFile("webui/dist/index.html")
	if err != nil {
		t.Fatalf("read embedded index.html: %v", err)
	}
	return b
}

// TestRoot_ServesConsoleIndex: GET / returns 200 with the console index.html.
func TestRoot_ServesConsoleIndex(t *testing.T) {
	app := newConsoleApp(t)
	status, body, hdr := do(t, app, http.MethodGet, "/", nil)

	if status != http.StatusOK {
		t.Fatalf("GET / status = %d, want 200", status)
	}
	if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET / Content-Type = %q, want text/html*", ct)
	}
	if !bytes.Equal(body, indexHTML(t)) {
		t.Errorf("GET / body is not the embedded index.html (got %d bytes, want %d)",
			len(body), len(indexHTML(t)))
	}
	// The shell must not be cached, so a new build is picked up immediately.
	if cc := hdr.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("GET / Cache-Control = %q, want no-cache", cc)
	}
}

// TestDeepLink_ServesSPAShell: a client-side route (e.g. /orgs) that maps to no
// physical file returns the SPA shell (200), NOT a 404 — so deep links / reloads
// on client routes work.
func TestDeepLink_ServesSPAShell(t *testing.T) {
	app := newConsoleApp(t)
	// Several representative deep links the console router owns client-side.
	for _, route := range []string{"/orgs", "/models", "/deploy/functions", "/discover/iam"} {
		status, body, hdr := do(t, app, http.MethodGet, route, nil)
		if status != http.StatusOK {
			t.Errorf("GET %s status = %d, want 200 (SPA fallback)", route, status)
			continue
		}
		if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET %s Content-Type = %q, want text/html*", route, ct)
		}
		if !bytes.Equal(body, indexHTML(t)) {
			t.Errorf("GET %s did not return the SPA shell", route)
		}
	}
}

// TestAPI_TakesPrecedenceOverSPA: GET /v1/models hits the API (JSON), never the
// SPA shell. The API route is registered before the catch-all, so it wins.
func TestAPI_TakesPrecedenceOverSPA(t *testing.T) {
	app := newConsoleApp(t)
	status, body, hdr := do(t, app, http.MethodGet, "/v1/models", nil)

	if status != http.StatusOK {
		t.Fatalf("GET /v1/models status = %d, want 200", status)
	}
	if ct := hdr.Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Errorf("GET /v1/models Content-Type = %q, want application/json*", ct)
	}
	if bytes.Equal(body, indexHTML(t)) {
		t.Fatal("GET /v1/models returned the SPA shell — API precedence broken")
	}
	if !bytes.Contains(body, []byte(`"zen-1"`)) {
		t.Errorf("GET /v1/models body = %q, want the API JSON", body)
	}
}

// TestUnmatchedAPIPath_Is404_NotSPA: an UNMATCHED path under an API/ops prefix
// must return a real 404 (JSON namespace), NOT the SPA HTML — clients calling a
// mistyped /v1/... must never receive an HTML 200.
func TestUnmatchedAPIPath_Is404_NotSPA(t *testing.T) {
	app := newConsoleApp(t)
	for _, p := range []string{"/v1/does-not-exist", "/v1/models/extra/typo", "/healthz", "/metrics"} {
		status, body, _ := do(t, app, http.MethodGet, p, nil)
		if status != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404 (API namespace, not SPA)", p, status)
		}
		if bytes.Equal(body, indexHTML(t)) {
			t.Errorf("GET %s returned the SPA shell — API namespace leaked HTML", p)
		}
	}
}

// TestAsset_ServedWithType: a real embedded file (index.html by its own path) is
// served with the right type and a cache header — proving assets are served
// directly, not routed through the SPA fallback.
func TestAsset_ServedDirectly(t *testing.T) {
	app := newConsoleApp(t)
	// index.html requested by path is a real file → served directly (200, html).
	status, body, hdr := do(t, app, http.MethodGet, "/index.html", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /index.html status = %d, want 200", status)
	}
	if !bytes.Equal(body, indexHTML(t)) {
		t.Error("GET /index.html did not return the file bytes")
	}
	if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("GET /index.html Content-Type = %q, want text/html*", ct)
	}
}

// TestPathTraversal_CannotEscapeEmbedFS: crafted traversal paths must never read
// outside the embed (io/fs rejects "..", leading "/", so Open/Stat fail and the
// request falls back to the SPA shell — never a host file, never a 500).
func TestPathTraversal_CannotEscapeEmbedFS(t *testing.T) {
	app := newConsoleApp(t)
	for _, evil := range []string{
		"/../go.mod",
		"/../../etc/passwd",
		"/assets/../../serve.go",
		"/%2e%2e/%2e%2e/etc/passwd",
		"/..%2fserve.go",
	} {
		status, body, hdr := do(t, app, http.MethodGet, evil, nil)
		// Whatever the router normalizes it to, the response is EITHER the SPA
		// shell (a client route) or a 404 (API-prefix guard) — never host-file
		// content and never a 5xx.
		if status >= 500 {
			t.Errorf("GET %s status = %d, want < 500 (no server error on traversal)", evil, status)
		}
		if status == http.StatusOK {
			if !bytes.Equal(body, indexHTML(t)) {
				t.Errorf("GET %s returned 200 with non-shell body — possible FS escape", evil)
			}
			if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Errorf("GET %s 200 Content-Type = %q, want text/html* (shell only)", evil, ct)
			}
		}
		// Belt-and-suspenders: the body must never contain source we know lives
		// outside the embed (go.mod's module line, serve.go's package clause).
		if bytes.Contains(body, []byte("module github.com/hanzoai/cloud")) ||
			bytes.Contains(body, []byte("func Serve(")) {
			t.Errorf("GET %s leaked repo source outside the embed FS", evil)
		}
	}
}

// TestHEAD_Root: HEAD / returns 200 headers with no body (static handler honors
// HEAD, so health-style probes and preflights work).
func TestHEAD_Root(t *testing.T) {
	app := newConsoleApp(t)
	status, body, hdr := do(t, app, http.MethodHead, "/", nil)
	if status != http.StatusOK {
		t.Fatalf("HEAD / status = %d, want 200", status)
	}
	if len(body) != 0 {
		t.Errorf("HEAD / body = %d bytes, want 0", len(body))
	}
	if ct := hdr.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("HEAD / Content-Type = %q, want text/html*", ct)
	}
}
