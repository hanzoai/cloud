package cloud

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/hanzoai/cloud/console"
	"github.com/zap-proto/zip"
)

func get(t *testing.T, app *zip.App, path string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest("GET", "http://x"+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

func TestTmpConsole(t *testing.T) {
	app := zip.New(zip.Config{})
	mountHealth(app)
	app.Raw(http.MethodGet, "/v1/kv/:key", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"k": c.Param("key")}) })
	if err := mountConsole(app); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		path   string
		status int
		ctype  string
		want   string
	}{
		{"/", 200, "text/html", "<title>cloud console</title>"},
		{"/console.css", 200, "text/css", "--accent"},
		{"/console.js", 200, "javascript", "openapi.json"},
		{"/fanclub", 200, "text/html", "<title>FanClub AI — Hanzo Cloud</title>"},
		{"/fanclub/", 200, "text/html", "<title>FanClub AI — Hanzo Cloud</title>"},
		{"/fanclub/index.html", 200, "text/html", "<title>FanClub AI — Hanzo Cloud</title>"},
		{"/fanclub/styles.css", 200, "text/css", "--lime"},
		{"/fanclub/app.js", 200, "javascript", "/v1/fanclub/config"},
		{"/kv", 200, "text/html", "<title>"},
		{"/deep/client/route", 200, "text/html", "console.js"},
		{"/../webui.go", 200, "text/html", "<title>"},
		{"/healthz", 200, "application/json", `{"status":"ok"}`},
		{"/readyz", 200, "application/json", `{"status":"ok"}`},
		{"/v1/nope", 404, "application/json", `{"error":{"code":"not_found","message":"no such route"}}`},
		{"/v1", 404, "application/json", "not_found"},
		{"/zap/x", 404, "application/json", "not_found"},
		{"/v1/kv/abc", 200, "application/json", `"k":"abc"`},
	}
	for _, c := range cases {
		res, body := get(t, app, c.path)
		if res.StatusCode != c.status {
			t.Errorf("%s: status %d want %d", c.path, res.StatusCode, c.status)
		}
		if ct := res.Header.Get("Content-Type"); !strings.Contains(ct, c.ctype) {
			t.Errorf("%s: content-type %q want %q", c.path, ct, c.ctype)
		}
		if !strings.Contains(body, c.want) {
			t.Errorf("%s: body %q missing %q", c.path, body[:min(len(body), 120)], c.want)
		}
	}

	if err := serveFS(zip.New(zip.Config{}), fstest.MapFS{"other.txt": {Data: []byte("x")}}); err == nil {
		t.Error("serveFS accepted an FS with no index.html")
	}

	_ = console.FS()
}

func TestPinnedWebMIMETypesAndMissingChunk404(t *testing.T) {
	bundle := fstest.MapFS{
		"index.html":                           {Data: []byte("<!doctype html><title>app</title>")},
		"_next/static/chunks/mod-DeKYn5x6.mjs": {Data: []byte("export const ok = true;")},
		"assets/shim-KSQZTTM6.cjs":             {Data: []byte("module.exports = 1;")},
		"assets/core.wasm":                     {Data: []byte{0x00, 0x61, 0x73, 0x6d}},
		"assets/app.css":                       {Data: []byte("body{color:#fff}")},
		"assets/logo.svg":                      {Data: []byte("<svg/>")},
		"assets/app.js.map":                    {Data: []byte("{}")},
	}
	app := zip.New(zip.Config{})
	if err := serveFS(app, bundle); err != nil {
		t.Fatalf("serveFS: %v", err)
	}

	for target, wantCT := range map[string]string{
		"/_next/static/chunks/mod-DeKYn5x6.mjs": "text/javascript; charset=utf-8",
		"/assets/shim-KSQZTTM6.cjs":             "text/javascript; charset=utf-8",
		"/assets/core.wasm":                     "application/wasm",
		"/assets/app.css":                       "text/css; charset=utf-8",
		"/assets/logo.svg":                      "image/svg+xml",
		"/assets/app.js.map":                    "application/json; charset=utf-8",
	} {
		res, _ := get(t, app, target)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", target, res.StatusCode)
		}
		if got := res.Header.Get("Content-Type"); got != wantCT {
			t.Errorf("GET %s Content-Type = %q, want %q", target, got, wantCT)
		}
	}

	// Missing chunks under bundler build-output subtrees must 404 (no-store)
	// instead of falling back to 200 text/html.
	for _, target := range []string{
		"/_next/static/chunks/stale-0j34vdo5b19hg.js",
		"/assets/index-DeKYn5x6.js",
		"/pay/_next/static/chunks/main.js",
		"/_astro/hoisted.abc123.js",
		"/_app/immutable/entry/app.js",
		"/v1/missing-endpoint",
	} {
		res, _ := get(t, app, target)
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s status = %d, want 404", target, res.StatusCode)
		}
		if got := res.Header.Get("Cache-Control"); got != "no-store, max-age=0" {
			t.Errorf("GET %s Cache-Control = %q, want %q", target, got, "no-store, max-age=0")
		}
	}
}
