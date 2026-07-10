// Copyright © 2026 Hanzo AI. MIT License.

// Package ui exposes the built admin-commerce (Hanzo GUI v7) bundle as
// an embedded filesystem.
//
// The bundle is produced externally in the @hanzo/gui workspace at
// ~/work/hanzo/gui/apps/admin-commerce (Vite + Hanzo GUI v7) and synced
// into ui/dist by scripts/sync-admin-ui.sh. The synced dist/ is baked
// into the commerced binary at compile time — no external static-assets
// directory, no sidecar, no separate deploy.
//
// Mount the returned handler at /_/commerce/ in the commerced HTTP
// router so it serves the SPA shell for every non-API request:
//
//	import commerceui "github.com/hanzoai/cloud/clients/commerce/ui"
//	mux.Handle("/_/commerce/", http.StripPrefix("/_/commerce", commerceui.Handler()))
//
// API routes under /v1/commerce/* take precedence via earlier route
// registration; anything else falls through to the SPA, which uses
// client-side routing so deep links survive reload.
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
// Empty when scripts/sync-admin-ui.sh has not been run.
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return distFS
	}
	return sub
}

// Handler returns an http.Handler that serves the embedded SPA.
//
// Behaviour:
//   - Hashed assets under /assets/ are cached forever (Vite emits
//     content-addressed names).
//   - index.html and other static files are served with no-cache.
//   - Anything that doesn't exist falls through to index.html so
//     react-router's BrowserRouter handles the deep link.
//   - If the build hasn't run and index.html is missing, every
//     request returns 503 so operators notice in staging before
//     shipping a blank image to production.
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
			serveIndex(w, root)
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

func serveIndex(w http.ResponseWriter, root fs.FS) {
	data, err := fs.ReadFile(root, "index.html")
	if err != nil {
		http.Error(w, "commerce admin UI not built (run scripts/sync-admin-ui.sh)", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}
