package webui

// Tests for the console: ONE binary serves the SPA at the web root AND the /v1
// API, from the same zip/fiber app. They drive real requests through the stack
// (app.Fiber().Test) exactly as production Serve wires it — a /v1 route registered
// FIRST, then Mount registered LAST — so the assertions prove the real precedence
// and SPA-fallback behavior, not a mock of it.
//
// The bundle is a FIXTURE now rather than the embed, and it is shaped like the
// real one: a static export's shell with the default brand baked into its <title>
// (what the white-label rewrite has to find) plus a bare-hash asset under
// _next/static (what Next.js actually emits).

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

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
	if err := Use(app, testBundle()); err != nil {
		t.Fatalf("Use:  %v", err)
	}
	return app
}

// shellHTML is the fixture SPA shell: a static export's <head> carrying the BAKED
// default-brand title, which is the thing brandTitle has to rewrite per host.
const shellHTML = `<!doctype html><html><head><meta charset="utf-8"><title>Hanzo Cloud Console</title></head><body><div id="__next"></div></body></html>`

// testBundle is a console bundle in the shape a published release carries.
func testBundle() fstest.MapFS {
	return fstest.MapFS{
		"index.html":                            {Data: []byte(shellHTML)},
		"_next/static/css/bdec3a94ead6ad5f.css": {Data: []byte("body{margin:0}")},
	}
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
		// Host is not a normal client header (net/http reads it from req.Host);
		// honor it so tests can drive the per-host white-label title.
		if strings.EqualFold(k, "Host") {
			req.Host = v
			continue
		}
		req.Header.Set(k, v)
	}
	// Timeout: 0 disables the per-request deadline (Fiber v3), so a slow CI host
	// never flakes these in-process router calls.
	resp, err := app.Test(req, zip.TestConfig{Timeout: 0})
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

// indexHTML is the shell the fixture bundle carries, so the assertions compare
// against the exact bytes the handler was given.
func indexHTML(t *testing.T) []byte {
	t.Helper()
	return []byte(shellHTML)
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
		t.Errorf("GET / body is not the bundle index.html (got %d bytes, want %d)",
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
	// NOTE: /metrics is intentionally a console page (dropped from apiPrefixes), so
	// it is NOT an API-namespace 404 — it serves the SPA like any console route.
	// "/health" is in the list because it was NOT in apiPrefixes, and the
	// catch-all answered it with 241 KB of console index.html on the live API —
	// a monitor asking whether the product is up was told about a static bundle.
	for _, p := range []string{"/v1/does-not-exist", "/v1/models/extra/typo", "/health", "/healthz"} {
		status, body, _ := do(t, app, http.MethodGet, p, nil)
		if status != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404 (API namespace, not SPA)", p, status)
		}
		if bytes.Equal(body, indexHTML(t)) {
			t.Errorf("GET %s returned the SPA shell — API namespace leaked HTML", p)
		}
	}
}

// TestAsset_ServedWithType: a real file in the bundle (index.html by its own path) is
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

// TestPathTraversal_CannotEscapeTheBundle: crafted traversal paths must never read
// outside the bundle (io/fs rejects "..", leading "/", so Open/Stat fail and the
// request falls back to the SPA shell — never a host file, never a 500).
func TestPathTraversal_CannotEscapeTheBundle(t *testing.T) {
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
		// outside the bundle (go.mod's module line, serve.go's package clause).
		if bytes.Contains(body, []byte("module github.com/hanzoai/cloud")) ||
			bytes.Contains(body, []byte("func Listen(")) {
			t.Errorf("GET %s leaked repo source outside the bundle", evil)
		}
	}
}

// TestRoot_TitleIsHostBranded: the console is a STATIC export whose
// <title> is baked to the default (Hanzo) brand at build time. serveIndex must
// rewrite it to the REQUEST host's white-label brand, so console.lux.cloud never
// renders "Hanzo Cloud Console" in the browser tab (a white-label violation) —
// while console.hanzo.ai still reads "Hanzo Cloud Console" (no regression). The
// SPA shell (GET / and every client-side deep link) carries the branded title.
func TestRoot_TitleIsHostBranded(t *testing.T) {
	app := newConsoleApp(t)
	cases := []struct{ host, wantTitle string }{
		{"console.lux.cloud", "Lux Cloud Console"},
		{"console.hanzo.ai", "Hanzo Cloud Console"},
		{"console.zoo.cloud", "Zoo Cloud Console"},
		{"api.lux.network", "Lux Cloud Console"}, // primary Domain match, not just .cloud
	}
	// GET / AND a representative client-side deep link both serve the shell.
	for _, target := range []string{"/", "/orgs"} {
		for _, tc := range cases {
			status, body, _ := do(t, app, http.MethodGet, target, map[string]string{"Host": tc.host})
			if status != http.StatusOK {
				t.Errorf("GET %s Host=%s status=%d, want 200", target, tc.host, status)
				continue
			}
			want := []byte("<title>" + tc.wantTitle + "</title>")
			if !bytes.Contains(body, want) {
				t.Errorf("GET %s Host=%s: served shell missing %s", target, tc.host, want)
			}
			// No host may leak a DIFFERENT brand's <title>.
			for _, other := range []string{"Hanzo Cloud Console", "Lux Cloud Console", "Zoo Cloud Console"} {
				if other != tc.wantTitle && bytes.Contains(body, []byte("<title>"+other+"</title>")) {
					t.Errorf("GET %s Host=%s leaked <title>%s</title>", target, tc.host, other)
				}
			}
		}
	}
}

// TestConsoleTitle: the pure host→title mapping. "<Brand> Cloud Console",
// matching hanzoai/console's `${brandName} Console`, with the Hanzo default for
// an unbranded host.
func TestConsoleTitle(t *testing.T) {
	cases := map[string]string{
		"console.lux.cloud":     "Lux Cloud Console",
		"console.hanzo.ai":      "Hanzo Cloud Console",
		"console.zoo.cloud":     "Zoo Cloud Console",
		"console.pars.ai":       "Pars Cloud Console",
		"api.lux.network":       "Lux Cloud Console",
		"lux.cloud":             "Lux Cloud Console",
		"console.lux.cloud:443": "Lux Cloud Console",   // port stripped
		"example.com":           "Hanzo Cloud Console", // unbranded → default
		"":                      "Hanzo Cloud Console", // empty → default
	}
	for host, want := range cases {
		if got := consoleTitle(host); got != want {
			t.Errorf("consoleTitle(%q) = %q, want %q", host, got, want)
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

// TestRouteShell_ServedForExportedRoutes: a deep load of a route the static
// export ships its OWN shell for (/signin → signin.html, /auth/callback →
// auth/callback.html) gets THAT shell — not index.html. Serving index for
// /auth/callback hydrates '/' instead, AuthGate discards the OAuth ?code and
// bounces to /signin: the login loop this pins against. Uses a synthetic FS —
// the committed fallback dist has no route shells; the real bundle does.
func TestRouteShell_ServedForExportedRoutes(t *testing.T) {
	fsys := fstest.MapFS{
		"index.html":         {Data: []byte("<html><head><title>Hanzo Cloud Console</title></head><body>ROOT</body></html>")},
		"signin.html":        {Data: []byte("<html><head><title>Hanzo Cloud Console</title></head><body>SIGNIN</body></html>")},
		"auth/callback.html": {Data: []byte("<html><head><title>Hanzo Cloud Console</title></head><body>CALLBACK</body></html>")},
	}
	h, err := newConsoleHandler(fsys, testDoor())
	if err != nil {
		t.Fatalf("newConsoleHandler: %v", err)
	}

	get := func(target, host string) (*httptest.ResponseRecorder, string) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if host != "" {
			req.Host = host
		}
		h.ServeHTTP(rec, req)
		return rec, rec.Body.String()
	}

	// The route's OWN shell, never cached.
	for target, marker := range map[string]string{
		"/auth/callback?code=abc&state=xyz": "CALLBACK",
		"/signin":                           "SIGNIN",
	} {
		rec, body := get(target, "")
		if rec.Code != http.StatusOK || !strings.Contains(body, marker) {
			t.Errorf("GET %s = %d %q, want 200 with the %s shell", target, rec.Code, body, marker)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("GET %s Cache-Control = %q, want no-cache", target, cc)
		}
	}

	// A route with no exported shell still falls back to index (deep links work).
	if _, body := get("/orgs", ""); !strings.Contains(body, "ROOT") {
		t.Errorf("GET /orgs did not fall back to the SPA shell")
	}

	// White-label: a brand host's route shell gets ITS title, not the baked one.
	if _, body := get("/signin", "console.lux.cloud"); strings.Contains(body, "Hanzo Cloud Console") {
		t.Errorf("route shell leaked the baked Hanzo title on a lux host: %q", body)
	}

	// A DIRECT .html request is also never cached (stale-deploy guard).
	if rec, _ := get("/signin.html", ""); rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("GET /signin.html Cache-Control = %q, want no-cache", rec.Header().Get("Cache-Control"))
	}
}

// A REGISTERED route still answers when this process has no console bundle.
//
// TestNoBundleIsA503_NotAShell already pins what the terminal handler says with
// no bytes (503 on a console path, 404 on an API namespace) and is not restated
// here. What that one cannot reach is the router: it drives the handler directly,
// so it says nothing about a real route mounted beside the catch-all.
//
// That is the property the public endpoint leans on. A console it cannot read used to
// abort the boot, so on 2026-08-15 one unreadable object answered every caller
// 503 — including everyone who never opens a browser. Serving on is only the
// better choice if the API genuinely survives, so the API is what this asserts.
func TestNoBundle_ARegisteredRouteStillAnswers(t *testing.T) {
	app := zip.New(zip.Config{})
	app.Get("/v1/models", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]any{"object": "list"})
	})
	if err := Use(app, nil); err != nil {
		t.Fatalf("Use(app, nil): %v — nil is the stated no-console case, not an error", err)
	}
	if code, _, _ := do(t, app, http.MethodGet, "/v1/models", nil); code != http.StatusOK {
		t.Fatalf("GET /v1/models = %d, want 200 — a missing console must not cost the API", code)
	}
}
