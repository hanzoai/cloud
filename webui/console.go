// Package webui serves the Hanzo Cloud console — the single-page app,
// white-labelled per request Host.
//
// It is a LEAF: stdlib + the brand registry + the fleet manifest's addresses +
// zip's net/http bridge, and NOTHING of package cloud (all three of those are
// themselves leaves). That is the whole reason it exists as its own package. The
// console has to be served from TWO places — every per-app plugin mounts it as
// its "/" catch-all through cloud.Listen, and the light host (cmd/cloud) owns "/"
// at the entry point and cannot import package cloud (that would relink the
// fleet it was split to avoid). One console implementation, reachable from both.
//
// IT CARRIES NO BYTES. The console used to be //go:embed'd from webui/dist, which
// the image build overwrote with the real static export — so the frontend's
// lifecycle was welded to this binary's: a CSS fix cost a ~22-minute cloud build
// plus a `strategy: Recreate` single-replica rollout, measured at 2m15s of
// api.hanzo.ai being down. The bytes are a PUBLISHED SITE RELEASE now (the same
// release subsystem that serves cd.hanzo.ai), read into memory by webui/release
// and handed in here as an fs.FS. A console release goes live in under a second
// and rolls back just as fast, with no build and no restart.
//
// What did NOT change is the HOST ROUTING: console.hanzo.ai is still this binary
// and still answers /v1 on the same origin, so the console's session cookie stays
// first-party. Only the source of the bytes moved. The sites edge deliberately
// does not own that host — a site host serves bytes and nothing else.
//
// This package decides how the bytes are SERVED: per-Host white-label <title>,
// static-export route shells, precompressed negotiation, cache policy by asset
// class, and the API-namespace 404 that keeps HTML out of a JSON caller's mouth.
package webui

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/brand"
	fiber "github.com/zap-proto/fiber/v3"
	zapmcp "github.com/zap-proto/mcp"
	"github.com/zap-proto/zip"
)

// apiPrefixes are the request-path prefixes the console catch-all must NEVER
// answer. They belong to the API/RPC/ops planes, which are registered on the
// app BEFORE the catch-all and therefore win for every route that actually
// exists. An UNMATCHED path under one of these prefixes (e.g. a typo'd
// /v1/does-not-exist) must return a real 404 from the API namespace — not the
// SPA shell — so clients never receive HTML where they expect JSON. Every other
// path is a client-side console route and falls back to index.html.
// NOTE: "/metrics" is intentionally NOT here. The Prometheus scrape surface lives
// on the SEPARATE ops listener (:9090, healthMux) — see serve.go. On the public
// product API (:8000) "/metrics" is a CONSOLE product page (the MetricsModule), so
// it must fall through to the SPA shell, not 404. One surface per port: :8000 =
// product API + SPA, :9090 = ops (health + Prometheus /metrics).
// "/api/" is listed for the opposite reason to the others: nothing serves it.
// The API is versioned at /v1/, so a caller on /api/ is on a prefix that does
// not exist and gets a 404 instead of a 200 of console HTML.
// "/health" sits beside "/healthz" because it is the same probe under the name
// a monitor reaches for first. Without it the catch-all below claimed the
// address and answered console HTML — 200 when the API was unreachable, and 503
// when only the static bundle was.
// "/.well-known/" is a PROTOCOL namespace, not a product one. RFC 8615 reserves
// it for discovery, and every client that reaches it — an OIDC relying party, an
// SDK generator, a crawler — parses the answer as JSON. Left out of this list the
// catch-all claimed the whole namespace, so an address nothing serves answered
// with the SPA: 200 of console HTML where the bundle is present, 503 where it is
// not. Both are worse than a 404, because a relying party reads them as "the
// issuer exists and is broken" rather than "that document is not here", and a
// generator that reads HTML as a spec emits an empty client AND REPORTS SUCCESS.
// The documents that ARE served here (openid-configuration,
// oauth-authorization-server, jwks, openapi.json) register before the catch-all
// and win as usual; this only decides the answer for the ones that do not.
// "/login/oauth" is a PROTOCOL namespace for the same reason "/.well-known/" is,
// and it failed the same way. It is the address IAM's authorization endpoint
// redirects a browser to, and iam claims it in its own prefix list — but the
// catch-all answered anything under it that iam did not serve, so an OAuth
// client on this host received the console shell. The console rendered its own
// "No such page" and the sign-in ended there, 200 the whole way, which is what
// made it invisible: `hanzo auth login` opened api.hanzo.ai, was handed HTML,
// and stopped. A person reads "No such page" as a broken link; a client reads
// 200 text/html as an authorization response it cannot parse. Neither can act
// on it, and a 404 would have said the true thing.
var apiPrefixes = []string{"/v1/", "/api/", "/zap", "/health", "/healthz", "/readyz", "/.well-known/", "/login/oauth"}

// consoleTitleRe matches the single <head> <title>…</title> element (any
// attributes, any inner text, across newlines) so serveIndex can rewrite it to
// the request host's white-label brand.
var consoleTitleRe = regexp.MustCompile(`(?is)<title[^>]*>.*?</title>`)

// consoleTitle is the white-label document <title> for a request Host:
// "<Brand> Cloud Console" (console.lux.cloud → "Lux Cloud Console",
// console.hanzo.ai → "Hanzo Cloud Console"). It mirrors hanzoai/console's own
// `${brandName} Console` SSR output so the served static console and the
// standalone app render an identical per-host tab title. Brand is resolved from
// the same brands registry as every other white-label surface (the brand leaf).
func consoleTitle(host string) string {
	return brand.Display(brand.ForHost(host)) + " Cloud Console"
}

// Mount registers the console at the web root as the app's terminal handler. Its
// caller registers it LAST — after every /v1 subsystem route, the /zap plane, and
// the health contract — so the router's in-order matching gives all real API
// routes precedence and only unmatched paths reach the SPA. Both cloud.Listen
// (every plugin) and cmd/cloud (the host process) call it, so the "/"
// catch-all is spelled ONE way.
//
// fsys is the console bundle, supplied by the CALLER — this package holds no
// bytes of its own (see the package doc). A nil fsys means this process serves no
// console: the catch-all still keeps the API namespaces honest and still answers
// the agent MCP address, and a console path gets a 503 that says so.
//
// app is not only where the catch-all is registered: it is also where this
// process's AGENT MCP SERVER comes from. zip.App.MCP is that server as a
// value — a frame in, a frame out — and the terminal handler answers with it
// at the address the server lives (see mcp.go). Nothing is configured and
// nothing is duplicated; the console is simply handed the server the app
// already has.
func Use(app *zip.App, fsys fs.FS) error {
	h, err := Handler(fsys, app.MCP, routerAllow(app))
	if err != nil {
		return err
	}
	app.All("/*", zip.AdaptNetHTTP(h))
	return nil
}

// Allow answers which methods this process serves at a path. An empty answer
// means the address itself is unserved.
//
// It exists because the terminal catch-all DESTROYS fiber's own 405. fiber
// computes that answer already (router.go `next`: when no route matches the
// request's method it re-walks the other method trees, appends each match to
// `Allow`, and returns ErrMethodNotAllowed instead of ErrNotFound) — but that
// code runs only when NOTHING matched, and `All("/*")` matches everything at
// every method. So the honest answer is computed and then thrown away, on every
// request that reaches here.
type Allow func(path string) []string

// routerAllow is that answer read off the ROUTER, which is the only thing that
// knows it. Nothing here re-implements matching: `fiber.RoutePatternMatch` is the
// router's own matcher, exported for exactly this ("checking potential matches
// without registering a route"), under the zero Config the router runs with —
// case-insensitive, non-strict. A second matcher written here would disagree with
// the router about a `:param` or a trailing slash, and disagree silently.
//
// `GetRoutes(true)` drops Use() middleware, so only endpoints are considered.
// The terminal catch-all is skipped BY ITS PATTERN, and that exclusion is what
// makes the whole thing work rather than being a tidy-up: `All("/*")` matches
// every method at every path, so leaving it in answers "every method is allowed"
// for every address in the fleet.
//
// It reads the table per call rather than caching, because a plugin may be
// mounted after Listen and a snapshot taken at Mount would answer for a router
// that no longer exists. The cost is paid only on a request already being
// refused.
func routerAllow(app *zip.App) Allow {
	return func(upath string) []string {
		var out []string
		for _, r := range app.Fiber().GetRoutes(true) {
			if r.Path == terminal || slices.Contains(out, r.Method) {
				continue
			}
			if fiber.RoutePatternMatch(upath, r.Path) {
				out = append(out, r.Method)
			}
		}
		slices.Sort(out)
		return out
	}
}

// terminal is the catch-all's pattern, spelled once: Mount registers it and
// routerAllow excludes it, and the two must name the same route.
const terminal = "/*"

// Handler is the console as a stdlib http.Handler (correct Content-Type,
// conditional GET, precompressed negotiation, SPA fallback) plus this process's
// agent MCP server — the form Mount adapts onto the zip router via
// zip.AdaptNetHTTP, and the form a test drives directly.
//
// allow may be nil, which means this handler cannot tell an unserved address from
// an unserved METHOD and answers 404 for both. That is the old behaviour, kept
// only for a caller with no router to ask.
func Handler(fsys fs.FS, mcp zapmcp.Handler, allow Allow) (http.Handler, error) {
	h, err := newConsoleHandler(fsys, mcp)
	if err != nil {
		return nil, err
	}
	h.allow = allow
	return h, nil
}

// consoleHandler serves a single-page app out of fsys: exact-file when it exists,
// index.html otherwise (deep-link fallback), and a real 404 for the API/ops
// namespaces so those never render as HTML.
//
// It holds NO copy of the shell. index.html used to be read once at startup,
// which was free while the bundle was baked into the binary and is wrong now that
// it is a live view of a site release: after a publish the cached shell would
// still name the PREVIOUS build's chunk hashes, so every script it asked for
// would miss and fall back to HTML. Reading the shell per request costs one copy
// of ~10KB out of RAM and is always the release that is actually mounted.
type consoleHandler struct {
	fsys fs.FS
	// mcp is this process's MCP server — zip's, handed in whole. The console does
	// not implement MCP and holds no tool list; it holds the one address a machine
	// calls and the value that answers there.
	mcp zapmcp.Handler
	// allow is the router's answer to "what methods serve this path". nil means
	// nobody can be asked, and then an unserved METHOD is indistinguishable from an
	// unserved ADDRESS — which is the whole defect this field exists to close.
	allow Allow
}

// newConsoleHandler proves the source is a console before anything serves from
// it: a bundle with no index.html has no shell to fall back to, so every
// client-side route would 404 and the failure would surface as a broken product
// rather than as a bad source. nil is the separate, stated case of "no bundle in
// this process" and is not an error.
//
// The handler is REQUIRED. A terminal handler with no MCP server cannot answer
// the one address in the process that is guaranteed not to be a console route,
// and the SPA fallback is the wrong answer there in the most damaging possible
// way — the bug this package was already carrying, pointing the other way.
func newConsoleHandler(fsys fs.FS, mcp zapmcp.Handler) (*consoleHandler, error) {
	if mcp == nil {
		return nil, fmt.Errorf("webui: no MCP server: the terminal handler answers one and cannot invent it")
	}
	if fsys == nil {
		return &consoleHandler{mcp: mcp}, nil
	}
	if _, err := fs.Stat(fsys, "index.html"); err != nil {
		// A POLLED source is allowed to be empty right now. It re-reads on an
		// interval, so "no shell yet" is a moment: mount it, and serveIndex answers
		// 503 until a poll fills it — the same 503 this handler already gives for a
		// bundle whose shell went away under a running process.
		//
		// Refusing it here is what turned one unreadable object into an outage that
		// outlived its own cause. The boot read failed, the composition root mounted
		// nothing AND skipped the watch loop, and the console stayed down after the
		// object was repaired because nothing was left to re-read it.
		//
		// A STATIC bundle with no shell is still refused, and that distinction is the
		// whole point: a baked bundle missing index.html is broken and every deep
		// link would 404, which is what source_test.go pins.
		if p, ok := fsys.(interface{ Polled() bool }); !ok || !p.Polled() {
			return nil, fmt.Errorf("webui: console source has no index.html: %w", err)
		}
	}
	return &consoleHandler{fsys: fsys, mcp: mcp}, nil
}

// methods is the guarded read of allow: no router to ask means no claim about the
// address, which is 404 — the honest answer when nothing can be established, and
// the behaviour every caller had before this existed.
func (h *consoleHandler) methods(upath string) []string {
	if h.allow == nil {
		return nil
	}
	return h.allow(upath)
}

func (h *consoleHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	upath := r.URL.Path
	if !strings.HasPrefix(upath, "/") {
		upath = "/" + upath
	}

	// The agent MCP server, BEFORE the static-console gate — an MCP client speaks
	// POST, and the gate below would have answered it "method not allowed" in the
	// console's own voice, which is how POST /mcp came to look like an endpoint
	// that exists but is misconfigured. This handler is TERMINAL, so it only ever
	// sees a path no route claimed in THIS process — which is why it is the right
	// place to answer the MCP request and the wrong place to guess where the
	// endpoint went. It answers with the process's OWN MCP server, so a plugin
	// whose route zip never mounted is served here and a host that claimed the
	// path with a signpost never arrives.
	if h.serveMCP(w, r, upath) {
		return
	}

	// API/ops namespaces: an unmatched path here is a real 404 in JSON, never
	// the SPA shell. (Matched API routes never reach this handler.)
	//
	// This runs BEFORE the static-console gate below, and the order is load-bearing
	// for the same reason the MCP rules are: WHAT the path is decides the answer,
	// not what method a browser would have used. With the gate first, a POST to an
	// unmatched API path was answered "method not allowed" — which tells a client
	// the endpoint EXISTS and it used the wrong verb, when in fact nothing serves
	// that address at all. 405 is a claim about an endpoint; only an endpoint may
	// make it.
	for _, p := range apiPrefixes {
		if upath != strings.TrimSuffix(p, "/") && !strings.HasPrefix(upath, p) {
			continue
		}
		// 405 WHEN THE ADDRESS EXISTS. Answering 404 to `GET /v1/chat/completions`
		// — a POST-only route that is registered, served and working — says the API
		// does not have that endpoint. It is the most expensive kind of wrong
		// answer, because the caller's next move is to go looking for a routing
		// bug that is not there: this exact 404 was reported twice as a missing
		// route on the fleet's largest product, and the model plane was fine both
		// times. RFC 9110 requires the Allow header on a 405, so the answer carries
		// what the address DOES take and the caller is one probe from done.
		if methods := h.methods(upath); len(methods) > 0 {
			w.Header().Set("Allow", strings.Join(methods, ", "))
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		// The console is static: only GET/HEAD. A non-GET that fell through to
		// here (every real API route already matched, and the API namespaces have
		// already 404'd above) is genuinely unhandled.
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// No bundle in this process, said plainly. The alternative — a blank shell, or
	// a placeholder that renders — is a page that LOOKS like the product and is
	// not, which is the one answer an entry point may never give. 503 also tells a
	// probe the truth: this address is meant to serve a console and cannot.
	if h.fsys == nil {
		http.Error(w, "console unavailable: this process serves no console bundle", http.StatusServiceUnavailable)
		return
	}

	name := path.Clean(strings.TrimPrefix(upath, "/"))
	if name == "" || name == "." {
		h.serveIndex(w, r)
		return
	}

	// Try the exact asset. Anything that isn't a real file (missing, or a
	// directory) is a client-side route → serve the SPA shell.
	if h.serveAsset(w, r, name) {
		return
	}
	// Static-export ROUTE shells: the console export emits one HTML per route
	// (/signin → signin.html, /auth/callback → auth/callback.html). A deep load
	// must get the ROUTE'S OWN shell so it hydrates the right page — serving
	// index.html for /auth/callback hydrates '/' instead, AuthGate discards the
	// ?code and bounces to /signin: the OAuth login loop. Only extensionless
	// paths are route candidates.
	if !strings.Contains(path.Base(name), ".") && h.serveRouteHTML(w, r, name+".html") {
		return
	}
	h.serveIndex(w, r)
}

// serveRouteHTML writes one exported shell — a route's own, or the index —
// no-cache (clients must pick up a new release immediately) with the white-label
// <title> rewritten for the request host. It is the ONE HTML-shell path: the
// index used to be a second copy of these five lines that read a cached buffer,
// which is how the two could disagree about a release. Returns false when the
// bundle has no HTML for name, so the caller falls back to the SPA shell.
func (h *consoleHandler) serveRouteHTML(w http.ResponseWriter, r *http.Request, name string) bool {
	b, err := fs.ReadFile(h.fsys, name)
	if err != nil {
		return false
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(brandTitle(b, r.Host))
	}
	return true
}

// serveAsset writes the bundle's file at name if it exists, negotiating a
// precompressed sibling (.br, then .gz) when the client accepts it and the build
// produced one. Returns false (writing nothing) when name is missing or a
// directory, so the caller can fall back to the SPA shell.
func (h *consoleHandler) serveAsset(w http.ResponseWriter, r *http.Request, name string) bool {
	info, err := fs.Stat(h.fsys, name)
	if err != nil || info.IsDir() {
		return false
	}

	// Precompressed negotiation: brotli wins over gzip when both are offered and
	// present (smaller wire). Fall through to identity otherwise. The response's
	// Content-Type is pinned from the LOGICAL name so a .js.br still advertises
	// application/javascript, and Content-Encoding tells the client to inflate.
	if enc, encName, ok := negotiateEncoding(h.fsys, name, r.Header.Get("Accept-Encoding")); ok {
		ef, err := h.fsys.Open(encName)
		if err == nil {
			defer ef.Close()
			w.Header().Set("Content-Type", contentType(name))
			w.Header().Set("Content-Encoding", enc)
			w.Header().Add("Vary", "Accept-Encoding")
			h.setCacheHeaders(w, name)
			w.WriteHeader(http.StatusOK)
			if r.Method != http.MethodHead {
				_, _ = io.Copy(w, ef)
			}
			return true
		}
	}

	f, err := h.fsys.Open(name)
	if err != nil {
		return false
	}
	defer f.Close()
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		return false
	}
	h.setCacheHeaders(w, name)
	// ServeContent sets Content-Type (by extension), handles conditional GET
	// (If-Modified-Since / If-None-Match) and range requests from the seeker.
	http.ServeContent(w, r, name, info.ModTime(), rs)
	return true
}

// serveIndex writes the SPA shell — the SAME treatment every other shell gets
// (serveRouteHTML), because it is one: no-cache, and the document <title>
// rewritten to the request host's white-label brand. The published shell is a
// STATIC export whose <title> is baked to the default (Hanzo) brand at BUILD
// time; a static export cannot read the request Host, so the SERVING layer
// injects the brand — otherwise a Lux/Zoo host leaks "Hanzo Cloud Console" in the
// browser tab, a white-label violation. The fingerprinted assets the shell
// references are cached hard by setCacheHeaders.
//
// The shell is proven present when the handler is built, so a miss here means it
// went away UNDER a running process — the one shape a mounted release cannot
// take (a swap installs a complete bundle or none). Answered as the outage it is,
// never as a 200 of nothing.
func (h *consoleHandler) serveIndex(w http.ResponseWriter, r *http.Request) {
	if h.serveRouteHTML(w, r, "index.html") {
		return
	}
	http.Error(w, "console unavailable: the bundle has no index.html", http.StatusServiceUnavailable)
}

// brandTitle rewrites a shell's <title> to the request host's white-label brand
// — ONE rewrite shared by the index shell and every per-route shell (a Lux/Zoo
// deep load must not leak the baked default brand either).
func brandTitle(shell []byte, host string) []byte {
	repl := []byte("<title>" + consoleTitle(host) + "</title>")
	loc := consoleTitleRe.FindIndex(shell)
	if loc == nil || bytes.Equal(shell[loc[0]:loc[1]], repl) {
		return shell
	}
	out := make([]byte, 0, len(shell)-(loc[1]-loc[0])+len(repl))
	out = append(out, shell[:loc[0]]...)
	out = append(out, repl...)
	out = append(out, shell[loc[1]:]...)
	return out
}

// setCacheHeaders applies cache policy by asset kind. Fingerprinted build assets
// (anything under assets/ or _next/ — Next/Vite emit content-hashed filenames
// there) are immutable and cached for a year; everything else gets a
// conservative default. index.html is handled in serveIndex (no-cache) and never
// reaches here.
func (h *consoleHandler) setCacheHeaders(w http.ResponseWriter, name string) {
	if strings.HasPrefix(name, "assets/") || strings.HasPrefix(name, "_next/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		return
	}
	// Any HTML shell (a directly-requested route .html) mirrors the index
	// policy: never cached, so a redeploy is picked up immediately.
	if strings.HasSuffix(name, ".html") {
		w.Header().Set("Cache-Control", "no-cache")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
}

// negotiateEncoding picks a precompressed sibling of name that the client
// accepts and that exists in fsys. Prefers brotli, then gzip. Returns the
// Content-Encoding token, the sibling's name, and whether one was chosen.
func negotiateEncoding(fsys fs.FS, name, acceptEncoding string) (enc, encName string, ok bool) {
	ae := strings.ToLower(acceptEncoding)
	if acceptsEncoding(ae, "br") {
		if n := name + ".br"; fileExists(fsys, n) {
			return "br", n, true
		}
	}
	if acceptsEncoding(ae, "gzip") {
		if n := name + ".gz"; fileExists(fsys, n) {
			return "gzip", n, true
		}
	}
	return "", "", false
}

// acceptsEncoding reports whether an Accept-Encoding value offers token, and not
// with an explicit q=0 (which disqualifies it). Sufficient for the two tokens we
// negotiate (br, gzip).
func acceptsEncoding(acceptEncoding, token string) bool {
	if !strings.Contains(acceptEncoding, token) {
		return false
	}
	for part := range strings.SplitSeq(acceptEncoding, ",") {
		part = strings.TrimSpace(part)
		if part == token || strings.HasPrefix(part, token+";") {
			// q=0 (exactly) disqualifies; q=0.x still counts.
			return !strings.Contains(part, "q=0") || strings.Contains(part, "q=0.")
		}
	}
	return false
}

func fileExists(fsys fs.FS, name string) bool {
	info, err := fs.Stat(fsys, name)
	return err == nil && !info.IsDir()
}

// contentType returns the MIME type for a logical asset name by extension,
// defaulting to octet-stream. Used when serving a precompressed body, where the
// on-disk extension (.br/.gz) would otherwise mislead type detection.
func contentType(name string) string {
	if ct := mime.TypeByExtension(path.Ext(name)); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

// compile-time assertion: consoleHandler is a stdlib handler (so it adapts onto
// the zip router via zip.AdaptNetHTTP).
var _ http.Handler = (*consoleHandler)(nil)
