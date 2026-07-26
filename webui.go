package cloud

import (
	"bytes"
	"embed"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"

	"github.com/zap-proto/zip"
)

// The unified console UI, embedded into the ONE cloud binary.
//
// `all:` embeds the whole tree (dotfiles included). At a plain `go build` this is
// the committed fallback shell (webui/dist/index.html); the build pipeline runs
// hanzoai/console's `npm run build:embed` (a static export) and OVERWRITES
// webui/dist with the real @hanzo/gui static bundle BEFORE `go build`, so the
// shipped binary carries the full console. That pipeline is the Dockerfile
// console stage for the image, and the `make webui` target for a standalone
// build (`make webui && go build ./cmd/cloud`, or `make build-standalone`).
// Either way there is exactly one artifact — no separate console Service, no
// second origin. See webui/dist/index.html, the Makefile, and the Dockerfile
// console-build stage.
//
//go:embed all:webui/dist
var consoleFS embed.FS

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
var apiPrefixes = []string{"/v1/", "/api/", "/zap", "/healthz", "/readyz"}

// consoleTitleRe matches the single <head> <title>…</title> element (any
// attributes, any inner text, across newlines) so serveIndex can rewrite it to
// the request host's white-label brand.
var consoleTitleRe = regexp.MustCompile(`(?is)<title[^>]*>.*?</title>`)

// consoleTitle is the white-label document <title> for a request Host:
// "<Brand> Cloud Console" (console.lux.cloud → "Lux Cloud Console",
// console.hanzo.ai → "Hanzo Cloud Console"). It mirrors hanzoai/console's own
// `${brandName} Console` SSR output so the embedded static console and the
// standalone app render an identical per-host tab title. Brand is resolved from
// the same brands registry as every other white-label surface (brand.go).
func consoleTitle(host string) string {
	return brandDisplay(BrandForHost(host)) + " Cloud Console"
}

// mountConsole registers the embedded console at the web root as the app's
// terminal handler. It is called LAST in Serve — after every /v1 subsystem
// route, the /zap plane, and the health contract — so Fiber's in-order matching
// gives all real API routes precedence and only unmatched paths reach the SPA.
//
// The console is served through a stdlib http.Handler (correct Content-Type,
// conditional GET, precompressed negotiation) adapted onto the zip router via
// zip.AdaptNetHTTP — the same net/http bridge the rest of the binary uses to
// mount stdlib surfaces. The SPA fallback mirrors the hanzoai/static plugin's
// SPAMode semantics (serve index.html for client routes); it is implemented in
// this one file rather than via that plugin because static.Handler is
// disk/S3-only today (its New() takes a Root path or S3 bucket, not an fs.FS),
// so it cannot serve an embed.FS. Teaching hanzoai/static to accept an fs.FS is
// the clean follow-up that lets this collapse onto the shared plugin.
func mountConsole(app *zip.App) error {
	sub, err := fs.Sub(consoleFS, "webui/dist")
	if err != nil {
		return err
	}
	h, err := newConsoleHandler(sub)
	if err != nil {
		return err
	}
	app.All("/*", zip.AdaptNetHTTP(h))
	return nil
}

// consoleHandler serves an embedded single-page app: exact-file when it exists,
// index.html otherwise (deep-link fallback), and a real 404 for the API/ops
// namespaces so those never render as HTML.
type consoleHandler struct {
	fsys  fs.FS
	index []byte // index.html, read once at startup (the SPA shell / fallback)
}

func newConsoleHandler(fsys fs.FS) (*consoleHandler, error) {
	index, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		return nil, err
	}
	return &consoleHandler{fsys: fsys, index: index}, nil
}

func (h *consoleHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		// The console is static: only GET/HEAD. A non-GET that fell through to
		// here (every real API route already matched) is genuinely unhandled.
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	upath := r.URL.Path
	if !strings.HasPrefix(upath, "/") {
		upath = "/" + upath
	}

	// API/ops namespaces: an unmatched path here is a real 404 in JSON, never
	// the SPA shell. (Matched API routes never reach this handler.)
	for _, p := range apiPrefixes {
		if upath == strings.TrimSuffix(p, "/") || strings.HasPrefix(upath, p) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
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

// serveRouteHTML writes a route's own exported shell with the SAME treatment as
// the index shell: no-cache (clients must pick up a new build immediately) and
// the white-label <title> rewrite. Returns false when the export has no HTML
// for the route, so the caller falls back to the SPA shell.
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

// serveAsset writes the embedded file at name if it exists, negotiating a
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

// serveIndex writes the SPA shell. index.html is never cached (clients must pick
// up a new build immediately); the fingerprinted assets it references are cached
// hard by setCacheHeaders. The shell's <title> is rewritten to the request
// host's white-label brand (indexFor).
func (h *consoleHandler) serveIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(h.indexFor(r.Host))
	}
}

// indexFor returns the SPA shell with its document <title> rewritten to the
// request host's white-label brand. The shipped shell is a STATIC export whose
// <title> is baked to the default (Hanzo) brand at BUILD time; a static export
// cannot read the request Host, so the SERVING layer injects the brand here —
// otherwise a Lux/Zoo host leaks "Hanzo Cloud Console" in the browser tab, a
// white-label violation. When the baked title already equals the brand title
// (the Hanzo/default host) or there is no <title> to rewrite, the embedded bytes
// are returned unchanged.
func (h *consoleHandler) indexFor(host string) []byte {
	return brandTitle(h.index, host)
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
	for _, part := range strings.Split(acceptEncoding, ",") {
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
