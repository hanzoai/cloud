// Package ui embeds the Hanzo Research R&D Ops Board — the dashboard over the
// /v1/research evidence plane (HIP-0512) — directly into the cloud binary and serves
// it at /research (cloud.hanzo.ai/research + console.hanzo.ai/research).
//
// ONE origin, no artifact: the board is served by the SAME process that serves
// /v1/research, so it reads the plane same-origin (GET /v1/research/projects · /totals
// · /experiments) with the caller's session, and falls back to an embedded snapshot
// when the plane is unreachable — it renders anywhere, then upgrades to live in place.
//
// dist/index.html is a single self-contained page (inline CSS + JS, zero external
// fetch beyond same-origin /v1/research), so the embed is one file. Mirrors the
// clients/tasks/ui embed pattern: mount under StripPrefix("/research", Handler()).
package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// FS returns the embedded built-UI filesystem rooted at dist/.
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Impossible at runtime: embed.FS entries are validated at compile time.
		return distFS
	}
	return sub
}

// Handler serves the embedded board. Mount it under StripPrefix("/research", …) so it
// sees root-relative paths. Any non-file path rewrites to index.html (SPA fallback);
// a missing bundle returns 503 so an absent build is loud, never a blank page.
func Handler() http.Handler {
	root := FS()
	fileServer := http.FileServer(http.FS(root))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		reqPath := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if reqPath == "" {
			reqPath = "index.html"
		}

		if _, err := fs.Stat(root, reqPath); err != nil {
			serveIndex(w, r, root)
			return
		}

		if strings.HasPrefix(reqPath, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// serveIndex writes index.html with no-cache so a freshly deployed board replaces the
// stale shell on the next request.
func serveIndex(w http.ResponseWriter, r *http.Request, root fs.FS) {
	data, err := fs.ReadFile(root, "index.html")
	if err != nil {
		http.Error(w, "research board not built (see clients/research/ui)", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
	_ = r
}
