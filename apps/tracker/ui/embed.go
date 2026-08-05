// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package ui embeds the built Hanzo Tracker SPA (@hanzo/tracker, the
// admin-tracker app in hanzoai/admin, Vite + hanzogui shell over the forge's
// own board CSS) into the cloud binary and serves it at /tracker/*.
//
// WHY cloud owns this embed: cloud is the ONE process that answers
// /v1/tracker (apps/tracker's per-org board store), so it serves the UI for
// that store too — one binary, one origin, one deploy. Same-origin is not
// cosmetic here: the SPA sends no tenancy of its own, because cloud's gateway
// mints the validated org from the IAM session BEFORE any tracker handler
// runs. A UI served from a second host would have to carry a token and assert
// an org, which is exactly the client-supplied tenancy the tracker refuses.
//
// This is what lets tracker.hanzo.ai serve a native board and retire the Huly
// tracker that answered that host.
//
// dist/ is the committed, content-addressed Vite build. The SPA is built with
// base '/tracker/' and API prefix '/v1/tracker' (see the admin-tracker
// vite.config), so every asset and XHR is same-origin under paths cloud
// already serves. To refresh it, rebuild the admin-tracker app and sync its
// dist/ here — see apps/tracker/ui/README.md.
package ui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// dist WITHOUT `all:` — the prefix that would also embed dot-files.
//
// dist/.sync-stamp is provenance for whoever regenerates this bundle: it names
// the source repository, branch and commit. That is a fact for the repo, not a
// file to publish, and `all:dist` published it — the SPA handler serves anything
// it can stat, so the build's private origin was readable by anyone who guessed
// the path. Plain `dist` omits every `.`-prefixed entry, so the stamp stays in
// git, stays truthful, and is not in the binary at all.
//
//go:embed dist
var distFS embed.FS

// FS returns the embedded built-UI filesystem rooted at dist/.
func FS() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		// Impossible at runtime: embed.FS entries are validated at compile
		// time. If dist/ were missing the binary would carry an empty FS.
		return distFS
	}
	return sub
}

// Handler returns an http.Handler that serves the embedded SPA. Mount it under
// StripPrefix("/tracker", …) so it sees root-relative paths.
//
//   - Content-addressed assets under assets/ ship immutable cache hints (Vite
//     hashes filenames).
//   - A path that is not a real file rewrites to index.html so the client-side
//     router handles the route — the standard SPA fallback that lets a deep-link
//     reload survive. EXCEPT under assets/, where a miss is a 404: that subtree
//     holds only content-addressed build output, so a request for a name that is
//     not there is a stale index pointing at a purged chunk. Answering the HTML
//     shell there hands a <script> tag a document — "Unexpected token '<'",
//     which reads as a corrupt bundle rather than the cache-miss it is.
//   - A DIRECTORY is not a page. http.FileServer lists one, so serving whatever
//     stat succeeds on published the whole asset manifest at /tracker/assets/;
//     a directory now falls through to the SPA (or 404s under assets/) and the
//     bundle's contents stay something you have to already know the name of.
//   - If the build is absent (index.html missing) every request returns 503, so
//     a missing bundle is loud in staging, never a blank page in production.
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
		asset := strings.HasPrefix(reqPath, assetDir+"/") || reqPath == assetDir

		info, err := fs.Stat(root, reqPath)
		switch {
		case err != nil || info.IsDir():
			// Not a file we serve. Under assets/ that is a miss and says so;
			// anywhere else it is a client-side route.
			if asset {
				http.NotFound(w, r)
				return
			}
			serveIndex(w, r, root)
			return
		case asset:
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		default:
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// assetDir is the one content-addressed subtree: Vite hashes every filename
// under it, which is what makes both the immutable cache hint and the 404-on-miss
// correct there and nowhere else.
const assetDir = "assets"

// serveIndex writes index.html with no-cache so a freshly-deployed build
// replaces the stale shell on the next request.
func serveIndex(w http.ResponseWriter, r *http.Request, root fs.FS) {
	data, err := fs.ReadFile(root, "index.html")
	if err != nil {
		http.Error(w, "tracker UI not built (see apps/tracker/ui/README.md)", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
	_ = r
}
