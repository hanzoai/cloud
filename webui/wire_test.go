package webui

// The console's wire, written down.
//
// Every probe below is rendered to text — status, every response header except
// Date, and the body — and compared against `wire`, a literal captured from the
// console as it was served. A change to this file's expectations is a change to
// what a browser, a monitor or an agent receives, so the table is the review
// surface: read the diff, not the code.
//
// The probes run through the real router (app.Test), because half of what they
// assert is the router's: the 405 and its Allow header are computed by walking
// the route table, and a registered route's precedence over the catch-all is a
// property of registration order. A handler driven directly can show neither.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/hanzoai/cloud/manifest"
	"github.com/zap-proto/zip"
)

// TestMain pins the four media types the table names. mime.TypeByExtension reads
// the host's mime.types on Linux, so an unpinned .js answers text/javascript on
// one machine and application/javascript on the next — a difference in the OS,
// not in the console. Both the identity path and the precompressed path read
// this same registry, so pinning it is neutral between them. The extensionless
// probe is left alone: its type comes from http.DetectContentType, which is Go.
func TestMain(m *testing.M) {
	for ext, ctype := range map[string]string{
		".css":  "text/css; charset=utf-8",
		".js":   "text/javascript; charset=utf-8",
		".html": "text/html; charset=utf-8",
		".txt":  "text/plain; charset=utf-8",
		".br":   "application/brotli",
	} {
		if err := mime.AddExtensionType(ext, ctype); err != nil {
			panic(err)
		}
	}
	os.Exit(m.Run())
}

// wireMod is the fixture bundle's modification time — one constant, so
// Last-Modified is a fixed string and a conditional GET has something exact to
// be conditional on.
var wireMod = time.Unix(1700000000, 0).UTC()

// wireModHTTP is that instant as a client sends it back.
var wireModHTTP = wireMod.Format(http.TimeFormat)

const (
	wireShell    = `<!doctype html><html><head><meta charset="utf-8"><title>Hanzo Cloud Console</title></head><body><div id="__next"></div></body></html>`
	wireSignin   = `<!doctype html><html><head><title>Hanzo Cloud Console</title></head><body>SIGNIN</body></html>`
	wireCallback = `<!doctype html><html><head><title>Hanzo Cloud Console</title></head><body>CALLBACK</body></html>`
	wireCSS      = `body{margin:0;padding:0}`
)

// wireFS is a console bundle shaped like a real static export: the SPA shell,
// two per-route shells, a fingerprinted asset with both precompressed siblings,
// an asset under assets/, an unfingerprinted file, and one file with no
// extension at all.
func wireFS() fstest.MapFS {
	f := func(s string) *fstest.MapFile {
		return &fstest.MapFile{Data: []byte(s), ModTime: wireMod}
	}
	return fstest.MapFS{
		"index.html":                     f(wireShell),
		"signin.html":                    f(wireSignin),
		"auth/callback.html":             f(wireCallback),
		"_next/static/css/a1b2c3.css":    f(wireCSS),
		"_next/static/css/a1b2c3.css.br": f("BROTLI-BODY"),
		"_next/static/css/a1b2c3.css.gz": f("GZIP-BODY"),
		"assets/app.js":                  f("console.log(1)"),
		"robots.txt":                     f("User-agent: *"),
		"LICENSE":                        f("Apache-2.0"),
	}
}

// wireApp is the host as cmd/cloud builds it: a POST-only route and a GET-only
// route to compute an Allow from, a typed op so zip has a registry to expose,
// the MCP route moved to manifest.MCPPath (so the framework default reaches the
// terminal handler, which is where the console answers it), and the console
// mounted LAST.
func wireApp(t *testing.T, fsys fs.FS) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{AppName: "cloud", DisableStartupMessage: true,
		MCP: zip.MCPConfig{Path: manifest.MCPPath}})
	app.Post("/v1/chat/completions", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{"id": "cmpl-1"})
	})
	app.Get("/v1/models", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{"object": "list"})
	})
	zip.Get[pingIn, pingOut](app, "/v1/probe/ping",
		func(ctx context.Context, in *pingIn) (*pingOut, error) { return &pingOut{OK: true}, nil })
	if err := Mount(app, fsys); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

type probe struct {
	name   string
	method string
	target string
	host   string
	header map[string]string
	body   string
}

// probes is the table. The order is the order the answers are rendered in, so
// it is also the order of the literal below.
var probes = []probe{
	// The API namespace: an unmatched path under one of these prefixes is a real
	// 404, never the shell. This is what keeps HTML out of a JSON caller's mouth.
	{name: "v1 miss", method: "GET", target: "/v1/does-not-exist"},
	{name: "v1 miss by POST", method: "POST", target: "/v1/does-not-exist"},
	{name: "v1 bare", method: "GET", target: "/v1"},
	{name: "v1 deep miss", method: "GET", target: "/v1/models/extra/typo"},
	{name: "api root", method: "GET", target: "/api/"},
	{name: "api path", method: "GET", target: "/api/v1/user"},
	{name: "api nonsense", method: "GET", target: "/api/totally-made-up"},
	{name: "health", method: "GET", target: "/health"},
	{name: "healthz", method: "GET", target: "/healthz"},
	{name: "readyz", method: "GET", target: "/readyz"},
	{name: "zap bare", method: "GET", target: "/zap"},
	{name: "zap subtree", method: "GET", target: "/zap/plane/thing"},
	{name: "v1 miss by HEAD", method: "HEAD", target: "/v1/nope"},

	// An address that exists for another method: 405 with the Allow the router
	// computes, never the 404 that says the endpoint is absent.
	{name: "post-only reached by GET", method: "GET", target: "/v1/chat/completions"},
	{name: "post-only reached by PUT", method: "PUT", target: "/v1/chat/completions"},
	{name: "get-only reached by DELETE", method: "DELETE", target: "/v1/models"},

	// A registered route still wins over the catch-all.
	{name: "registered GET", method: "GET", target: "/v1/models"},
	{name: "registered POST", method: "POST", target: "/v1/chat/completions"},

	// The agent MCP server, answered by the terminal handler at the framework
	// default because this app moved its route to manifest.MCPPath.
	{name: "mcp tools/list", method: "POST", target: "/mcp",
		header: map[string]string{"Content-Type": "application/json"},
		body:   `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`},
	{name: "mcp initialize", method: "POST", target: "/mcp",
		header: map[string]string{"Content-Type": "application/json"},
		body:   `{"jsonrpc":"2.0","id":2,"method":"initialize","params":{}}`},
	{name: "mcp parse error", method: "POST", target: "/mcp",
		header: map[string]string{"Content-Type": "application/json"}, body: `{`},
	{name: "mcp GET", method: "GET", target: "/mcp"},
	{name: "mcp HEAD", method: "HEAD", target: "/mcp"},
	{name: "mcp canonical GET", method: "GET", target: manifest.MCPPath},
	{name: "mcp-prefixed console route", method: "GET", target: "/mcp-servers"},

	// The shells, and the per-Host <title> a static export cannot bake.
	{name: "root hanzo", method: "GET", target: "/", host: "console.hanzo.ai"},
	{name: "root lux", method: "GET", target: "/", host: "console.lux.cloud"},
	{name: "root zoo", method: "GET", target: "/", host: "console.zoo.cloud"},
	{name: "deep link lux", method: "GET", target: "/orgs", host: "console.lux.cloud"},
	{name: "root HEAD", method: "HEAD", target: "/", host: "console.hanzo.ai"},
	{name: "index by path", method: "GET", target: "/index.html", host: "console.hanzo.ai"},

	// The per-route static-export shells. Collapsing these into index.html
	// hydrates '/' for /auth/callback, AuthGate discards the ?code, and the OAuth
	// login loop is back.
	{name: "signin shell", method: "GET", target: "/signin", host: "console.hanzo.ai"},
	{name: "signin shell lux", method: "GET", target: "/signin", host: "console.lux.cloud"},
	{name: "callback shell", method: "GET", target: "/auth/callback?code=abc&state=xyz", host: "console.hanzo.ai"},
	{name: "signin by filename", method: "GET", target: "/signin.html", host: "console.hanzo.ai"},

	// Assets: type, cache class, precompressed negotiation.
	{name: "css identity", method: "GET", target: "/_next/static/css/a1b2c3.css"},
	{name: "css brotli", method: "GET", target: "/_next/static/css/a1b2c3.css",
		header: map[string]string{"Accept-Encoding": "br, gzip"}},
	{name: "css gzip", method: "GET", target: "/_next/static/css/a1b2c3.css",
		header: map[string]string{"Accept-Encoding": "gzip"}},
	{name: "css brotli refused", method: "GET", target: "/_next/static/css/a1b2c3.css",
		header: map[string]string{"Accept-Encoding": "br;q=0, gzip"}},
	{name: "css brotli HEAD", method: "HEAD", target: "/_next/static/css/a1b2c3.css",
		header: map[string]string{"Accept-Encoding": "br"}},
	{name: "css sibling by name", method: "GET", target: "/_next/static/css/a1b2c3.css.br"},
	{name: "assets js", method: "GET", target: "/assets/app.js"},
	{name: "unfingerprinted file", method: "GET", target: "/robots.txt"},
	{name: "no extension", method: "GET", target: "/LICENSE"},
	{name: "directory falls to shell", method: "GET", target: "/_next/static", host: "console.hanzo.ai"},

	// Conditional GET and ranges on an asset.
	{name: "css head", method: "HEAD", target: "/_next/static/css/a1b2c3.css"},
	{name: "css not modified", method: "GET", target: "/_next/static/css/a1b2c3.css",
		header: map[string]string{"If-Modified-Since": wireModHTTP}},
	{name: "css range prefix", method: "GET", target: "/_next/static/css/a1b2c3.css",
		header: map[string]string{"Range": "bytes=0-3"}},
	{name: "css range open", method: "GET", target: "/_next/static/css/a1b2c3.css",
		header: map[string]string{"Range": "bytes=5-"}},
	{name: "css range suffix", method: "GET", target: "/_next/static/css/a1b2c3.css",
		header: map[string]string{"Range": "bytes=-4"}},
	{name: "css range unsatisfiable", method: "GET", target: "/_next/static/css/a1b2c3.css",
		header: map[string]string{"Range": "bytes=900-999"}},
	{name: "css if-range fresh", method: "GET", target: "/_next/static/css/a1b2c3.css",
		header: map[string]string{"Range": "bytes=0-3", "If-Range": wireModHTTP}},
	{name: "css if-range stale", method: "GET", target: "/_next/static/css/a1b2c3.css",
		header: map[string]string{"Range": "bytes=0-3", "If-Range": "Mon, 02 Jan 2006 15:04:05 GMT"}},

	// A console path is GET/HEAD only, and a traversal reads nothing.
	{name: "console route by POST", method: "POST", target: "/orgs"},
	{name: "traversal", method: "GET", target: "/../go.mod", host: "console.hanzo.ai"},
}

// noBundleProbes run against a process that mounted no console — every per-app
// plugin binary. The API namespace answers exactly as before; a console path is
// an honest 503 rather than a page that looks like the product and is not.
var noBundleProbes = []probe{
	{name: "no bundle console path", method: "GET", target: "/orgs"},
	{name: "no bundle root", method: "GET", target: "/"},
	{name: "no bundle api miss", method: "GET", target: "/v1/does-not-exist"},
	{name: "no bundle 405", method: "GET", target: "/v1/chat/completions"},
	{name: "no bundle asset", method: "GET", target: "/assets/app.js"},
}

// render issues one probe and writes its whole answer as text. Date is the only
// header dropped: it is the clock, not the console.
func render(t *testing.T, app *zip.App, p probe) string {
	t.Helper()
	var body io.Reader
	if p.body != "" {
		body = strings.NewReader(p.body)
	}
	req, err := http.NewRequest(p.method, p.target, body)
	if err != nil {
		t.Fatalf("%s: new request: %v", p.name, err)
	}
	if p.host != "" {
		req.Host = p.host
	}
	for k, v := range p.header {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 0})
	if err != nil {
		t.Fatalf("%s: %v", p.name, err)
	}
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("%s: read body: %v", p.name, err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s %s %s\n", p.name, p.method, p.target)
	fmt.Fprintf(&b, "  %d\n", resp.StatusCode)
	names := make([]string, 0, len(resp.Header))
	for k := range resp.Header {
		if k != "Date" {
			names = append(names, k)
		}
	}
	sort.Strings(names)
	for _, k := range names {
		for _, v := range resp.Header[k] {
			fmt.Fprintf(&b, "  %s: %s\n", k, v)
		}
	}
	if len(got) > 1024 {
		fmt.Fprintf(&b, "  body %d bytes sha256 %x\n", len(got), sha256.Sum256(got))
	} else {
		fmt.Fprintf(&b, "  body %q\n", got)
	}
	return b.String()
}

// TestTheWire is the whole contract in one comparison.
func TestTheWire(t *testing.T) {
	var b strings.Builder
	app := wireApp(t, wireFS())
	for _, p := range probes {
		b.WriteString(render(t, app, p))
	}
	bare := wireApp(t, nil) // fs.FS(nil): the stated no-console case
	for _, p := range noBundleProbes {
		b.WriteString(render(t, bare, p))
	}
	if got := b.String(); got != wire {
		t.Errorf("the console's wire moved.\n--- want ---\n%s\n--- got ---\n%s", wire, got)
	}
}

// wire is the console's answer to every probe above. It was captured from the
// console as it was served and is not regenerated: a diff here is a change a
// client can see.
const wire = `v1 miss GET /v1/does-not-exist
  404
  Content-Type: text/plain; charset=utf-8
  Link: </v1/does-not-exist>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "not found\n"
v1 miss by POST POST /v1/does-not-exist
  404
  Content-Type: text/plain; charset=utf-8
  Link: </v1/does-not-exist>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "not found\n"
v1 bare GET /v1
  404
  Content-Type: text/plain; charset=utf-8
  Link: </v1>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "not found\n"
v1 deep miss GET /v1/models/extra/typo
  404
  Content-Type: text/plain; charset=utf-8
  Link: </v1/models/extra/typo>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "not found\n"
api root GET /api/
  404
  Content-Type: text/plain; charset=utf-8
  Link: </api/>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "not found\n"
api path GET /api/v1/user
  404
  Content-Type: text/plain; charset=utf-8
  Link: </api/v1/user>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "not found\n"
api nonsense GET /api/totally-made-up
  404
  Content-Type: text/plain; charset=utf-8
  Link: </api/totally-made-up>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "not found\n"
health GET /health
  404
  Content-Type: text/plain; charset=utf-8
  Link: </health>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "not found\n"
healthz GET /healthz
  404
  Content-Type: text/plain; charset=utf-8
  Link: </healthz>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "not found\n"
readyz GET /readyz
  404
  Content-Type: text/plain; charset=utf-8
  Link: </readyz>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "not found\n"
zap bare GET /zap
  404
  Content-Type: text/plain; charset=utf-8
  Link: </zap>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "not found\n"
zap subtree GET /zap/plane/thing
  404
  Content-Type: text/plain; charset=utf-8
  Link: </zap/plane/thing>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "not found\n"
v1 miss by HEAD HEAD /v1/nope
  404
  Content-Type: text/plain; charset=utf-8
  Link: </v1/nope>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body ""
post-only reached by GET GET /v1/chat/completions
  405
  Allow: POST
  Content-Type: text/plain; charset=utf-8
  Link: </v1/chat/completions>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "method not allowed\n"
post-only reached by PUT PUT /v1/chat/completions
  405
  Allow: POST
  Content-Type: text/plain; charset=utf-8
  Link: </v1/chat/completions>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "method not allowed\n"
get-only reached by DELETE DELETE /v1/models
  405
  Allow: GET, HEAD
  Content-Type: text/plain; charset=utf-8
  Link: </v1/models>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "method not allowed\n"
registered GET GET /v1/models
  200
  Content-Length: 17
  Content-Type: application/json; charset=utf-8
  Link: </v1/models>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "{\"object\":\"list\"}"
registered POST POST /v1/chat/completions
  200
  Content-Length: 15
  Content-Type: application/json; charset=utf-8
  Link: </v1/chat/completions>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "{\"id\":\"cmpl-1\"}"
mcp tools/list POST /mcp
  200
  Content-Type: application/json
  Link: </mcp>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"tools\":[{\"annotations\":{\"readOnlyHint\":true},\"description\":\"\",\"inputSchema\":{\"properties\":{},\"type\":\"object\"},\"name\":\"get_probe_ping\"}]}}"
mcp initialize POST /mcp
  200
  Content-Type: application/json
  Link: </mcp>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "{\"jsonrpc\":\"2.0\",\"id\":2,\"result\":{\"capabilities\":{\"tools\":{\"listChanged\":false}},\"protocolVersion\":\"2026-07-28\",\"serverInfo\":{\"name\":\"cloud\",\"version\":\"\"}}}"
mcp parse error POST /mcp
  200
  Content-Type: application/json
  Link: </mcp>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":-32700,\"message\":\"parse error\"}}"
mcp GET GET /mcp
  405
  Allow: POST
  Content-Type: application/json; charset=utf-8
  Link: </mcp>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "{\"error\":\"the MCP server speaks JSON-RPC over POST\",\"door\":\"/mcp\"}"
mcp HEAD HEAD /mcp
  405
  Allow: POST
  Content-Type: application/json; charset=utf-8
  Link: </mcp>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body ""
mcp canonical GET GET /v1/mcp
  405
  Allow: POST
  Content-Type: application/json; charset=utf-8
  Link: </v1/mcp>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "{\"error\":\"the MCP server speaks JSON-RPC over POST\",\"door\":\"/v1/mcp\"}"
mcp-prefixed console route GET /mcp-servers
  200
  Cache-Control: no-cache
  Content-Type: text/html; charset=utf-8
  Link: </mcp-servers>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "<!doctype html><html><head><meta charset=\"utf-8\"><title>Hanzo Cloud Console</title></head><body><div id=\"__next\"></div></body></html>"
root hanzo GET /
  200
  Cache-Control: no-cache
  Content-Type: text/html; charset=utf-8
  Link: </>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "<!doctype html><html><head><meta charset=\"utf-8\"><title>Hanzo Cloud Console</title></head><body><div id=\"__next\"></div></body></html>"
root lux GET /
  200
  Cache-Control: no-cache
  Content-Type: text/html; charset=utf-8
  Link: </>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "<!doctype html><html><head><meta charset=\"utf-8\"><title>Lux Cloud Console</title></head><body><div id=\"__next\"></div></body></html>"
root zoo GET /
  200
  Cache-Control: no-cache
  Content-Type: text/html; charset=utf-8
  Link: </>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "<!doctype html><html><head><meta charset=\"utf-8\"><title>Zoo Cloud Console</title></head><body><div id=\"__next\"></div></body></html>"
deep link lux GET /orgs
  200
  Cache-Control: no-cache
  Content-Type: text/html; charset=utf-8
  Link: </orgs>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "<!doctype html><html><head><meta charset=\"utf-8\"><title>Lux Cloud Console</title></head><body><div id=\"__next\"></div></body></html>"
root HEAD HEAD /
  200
  Cache-Control: no-cache
  Content-Type: text/html; charset=utf-8
  Link: </>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body ""
index by path GET /index.html
  200
  Accept-Ranges: bytes
  Cache-Control: no-cache
  Content-Length: 133
  Content-Type: text/html; charset=utf-8
  Last-Modified: Tue, 14 Nov 2023 22:13:20 GMT
  Link: </index.html>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "<!doctype html><html><head><meta charset=\"utf-8\"><title>Hanzo Cloud Console</title></head><body><div id=\"__next\"></div></body></html>"
signin shell GET /signin
  200
  Cache-Control: no-cache
  Content-Type: text/html; charset=utf-8
  Link: </signin>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "<!doctype html><html><head><title>Hanzo Cloud Console</title></head><body>SIGNIN</body></html>"
signin shell lux GET /signin
  200
  Cache-Control: no-cache
  Content-Type: text/html; charset=utf-8
  Link: </signin>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "<!doctype html><html><head><title>Lux Cloud Console</title></head><body>SIGNIN</body></html>"
callback shell GET /auth/callback?code=abc&state=xyz
  200
  Cache-Control: no-cache
  Content-Type: text/html; charset=utf-8
  Link: </auth/callback>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "<!doctype html><html><head><title>Hanzo Cloud Console</title></head><body>CALLBACK</body></html>"
signin by filename GET /signin.html
  200
  Accept-Ranges: bytes
  Cache-Control: no-cache
  Content-Length: 94
  Content-Type: text/html; charset=utf-8
  Last-Modified: Tue, 14 Nov 2023 22:13:20 GMT
  Link: </signin.html>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "<!doctype html><html><head><title>Hanzo Cloud Console</title></head><body>SIGNIN</body></html>"
css identity GET /_next/static/css/a1b2c3.css
  200
  Accept-Ranges: bytes
  Cache-Control: public, max-age=31536000, immutable
  Content-Length: 24
  Content-Type: text/css; charset=utf-8
  Last-Modified: Tue, 14 Nov 2023 22:13:20 GMT
  Link: </_next/static/css/a1b2c3.css>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "body{margin:0;padding:0}"
css brotli GET /_next/static/css/a1b2c3.css
  200
  Cache-Control: public, max-age=31536000, immutable
  Content-Encoding: br
  Content-Type: text/css; charset=utf-8
  Link: </_next/static/css/a1b2c3.css>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  Vary: Accept-Encoding
  body "BROTLI-BODY"
css gzip GET /_next/static/css/a1b2c3.css
  200
  Cache-Control: public, max-age=31536000, immutable
  Content-Encoding: gzip
  Content-Type: text/css; charset=utf-8
  Link: </_next/static/css/a1b2c3.css>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  Vary: Accept-Encoding
  body "GZIP-BODY"
css brotli refused GET /_next/static/css/a1b2c3.css
  200
  Cache-Control: public, max-age=31536000, immutable
  Content-Encoding: gzip
  Content-Type: text/css; charset=utf-8
  Link: </_next/static/css/a1b2c3.css>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  Vary: Accept-Encoding
  body "GZIP-BODY"
css brotli HEAD HEAD /_next/static/css/a1b2c3.css
  200
  Cache-Control: public, max-age=31536000, immutable
  Content-Encoding: br
  Content-Type: text/css; charset=utf-8
  Link: </_next/static/css/a1b2c3.css>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  Vary: Accept-Encoding
  body ""
css sibling by name GET /_next/static/css/a1b2c3.css.br
  200
  Accept-Ranges: bytes
  Cache-Control: public, max-age=31536000, immutable
  Content-Length: 11
  Content-Type: application/brotli
  Last-Modified: Tue, 14 Nov 2023 22:13:20 GMT
  Link: </_next/static/css/a1b2c3.css.br>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "BROTLI-BODY"
assets js GET /assets/app.js
  200
  Accept-Ranges: bytes
  Cache-Control: public, max-age=31536000, immutable
  Content-Length: 14
  Content-Type: text/javascript; charset=utf-8
  Last-Modified: Tue, 14 Nov 2023 22:13:20 GMT
  Link: </assets/app.js>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "console.log(1)"
unfingerprinted file GET /robots.txt
  200
  Accept-Ranges: bytes
  Cache-Control: public, max-age=3600
  Content-Length: 13
  Content-Type: text/plain; charset=utf-8
  Last-Modified: Tue, 14 Nov 2023 22:13:20 GMT
  Link: </robots.txt>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "User-agent: *"
no extension GET /LICENSE
  200
  Accept-Ranges: bytes
  Cache-Control: public, max-age=3600
  Content-Length: 10
  Content-Type: text/plain; charset=utf-8
  Last-Modified: Tue, 14 Nov 2023 22:13:20 GMT
  Link: </LICENSE>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "Apache-2.0"
directory falls to shell GET /_next/static
  200
  Cache-Control: no-cache
  Content-Type: text/html; charset=utf-8
  Link: </_next/static>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "<!doctype html><html><head><meta charset=\"utf-8\"><title>Hanzo Cloud Console</title></head><body><div id=\"__next\"></div></body></html>"
css head HEAD /_next/static/css/a1b2c3.css
  200
  Accept-Ranges: bytes
  Cache-Control: public, max-age=31536000, immutable
  Content-Length: 24
  Content-Type: text/css; charset=utf-8
  Last-Modified: Tue, 14 Nov 2023 22:13:20 GMT
  Link: </_next/static/css/a1b2c3.css>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body ""
css not modified GET /_next/static/css/a1b2c3.css
  304
  Cache-Control: public, max-age=31536000, immutable
  Last-Modified: Tue, 14 Nov 2023 22:13:20 GMT
  Link: </_next/static/css/a1b2c3.css>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body ""
css range prefix GET /_next/static/css/a1b2c3.css
  206
  Accept-Ranges: bytes
  Cache-Control: public, max-age=31536000, immutable
  Content-Length: 4
  Content-Range: bytes 0-3/24
  Content-Type: text/css; charset=utf-8
  Last-Modified: Tue, 14 Nov 2023 22:13:20 GMT
  Link: </_next/static/css/a1b2c3.css>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "body"
css range open GET /_next/static/css/a1b2c3.css
  206
  Accept-Ranges: bytes
  Cache-Control: public, max-age=31536000, immutable
  Content-Length: 19
  Content-Range: bytes 5-23/24
  Content-Type: text/css; charset=utf-8
  Last-Modified: Tue, 14 Nov 2023 22:13:20 GMT
  Link: </_next/static/css/a1b2c3.css>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "margin:0;padding:0}"
css range suffix GET /_next/static/css/a1b2c3.css
  206
  Accept-Ranges: bytes
  Cache-Control: public, max-age=31536000, immutable
  Content-Length: 4
  Content-Range: bytes 20-23/24
  Content-Type: text/css; charset=utf-8
  Last-Modified: Tue, 14 Nov 2023 22:13:20 GMT
  Link: </_next/static/css/a1b2c3.css>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "g:0}"
css range unsatisfiable GET /_next/static/css/a1b2c3.css
  416
  Content-Range: bytes */24
  Content-Type: text/plain; charset=utf-8
  Link: </_next/static/css/a1b2c3.css>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "invalid range: failed to overlap\n"
css if-range fresh GET /_next/static/css/a1b2c3.css
  206
  Accept-Ranges: bytes
  Cache-Control: public, max-age=31536000, immutable
  Content-Length: 4
  Content-Range: bytes 0-3/24
  Content-Type: text/css; charset=utf-8
  Last-Modified: Tue, 14 Nov 2023 22:13:20 GMT
  Link: </_next/static/css/a1b2c3.css>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "body"
css if-range stale GET /_next/static/css/a1b2c3.css
  200
  Accept-Ranges: bytes
  Cache-Control: public, max-age=31536000, immutable
  Content-Length: 24
  Content-Type: text/css; charset=utf-8
  Last-Modified: Tue, 14 Nov 2023 22:13:20 GMT
  Link: </_next/static/css/a1b2c3.css>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "body{margin:0;padding:0}"
console route by POST POST /orgs
  405
  Content-Type: text/plain; charset=utf-8
  Link: </orgs>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "method not allowed\n"
traversal GET /../go.mod
  200
  Cache-Control: no-cache
  Content-Type: text/html; charset=utf-8
  Link: </../go.mod>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  body "<!doctype html><html><head><meta charset=\"utf-8\"><title>Hanzo Cloud Console</title></head><body><div id=\"__next\"></div></body></html>"
no bundle console path GET /orgs
  503
  Content-Type: text/plain; charset=utf-8
  Link: </orgs>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "console unavailable: this process serves no console bundle\n"
no bundle root GET /
  503
  Content-Type: text/plain; charset=utf-8
  Link: </>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "console unavailable: this process serves no console bundle\n"
no bundle api miss GET /v1/does-not-exist
  404
  Content-Type: text/plain; charset=utf-8
  Link: </v1/does-not-exist>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "not found\n"
no bundle 405 GET /v1/chat/completions
  405
  Allow: POST
  Content-Type: text/plain; charset=utf-8
  Link: </v1/chat/completions>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "method not allowed\n"
no bundle asset GET /assets/app.js
  503
  Content-Type: text/plain; charset=utf-8
  Link: </assets/app.js>; rel="self"
  Link: </.well-known/openapi.json>; rel="service-desc"
  Link: </docs>; rel="service-doc"
  Server: zip
  X-Content-Type-Options: nosniff
  body "console unavailable: this process serves no console bundle\n"
`
