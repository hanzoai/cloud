// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package spa serves a built single-page app out of an embedded filesystem.
//
// It is the ONE way an app in this repo serves its own UI. Three apps had
// byte-identical copies of this handler (tasks, research, and meet as it landed);
// the policy in it is not obvious enough to be worth restating three times, and a
// policy restated three times drifts:
//
//   - a content-addressed asset under assets/ carries an immutable cache hint,
//     because Vite hashes the filename — the bytes at that name never change;
//   - everything else is no-cache, so a freshly deployed shell replaces the stale
//     one on the next request rather than on the next cache expiry;
//   - a path that is not a real file rewrites to index.html, which is what lets a
//     deep link survive a reload under a client-side router — EXCEPT under
//     assets/, where a miss is a 404. That subtree holds only content-addressed
//     build output, so a name that is not there is a stale shell asking for a
//     purged chunk, never a client-side route. Answering the shell there hands a
//     <script> tag a document — "Unexpected token '<'", which reads as a corrupt
//     bundle rather than the cache miss it is;
//   - a DIRECTORY is not a page. http.FileServer lists one, so serving whatever
//     stat succeeds on publishes the whole asset manifest at /<app>/assets/;
//     a directory falls through to the shell (or 404s under assets/) instead;
//   - a MISSING index.html is 503, not a blank 200. A bundle that failed to sync
//     is then loud in staging instead of being a white page in production.
//
// It is NOT the console at "/" (webui). That one is a different handler on
// purpose: it owns the root catch-all, so it has to refuse the API namespaces
// outright and rewrite the document title per white-label host. This one is
// mounted under its app's own prefix and never sees a path that is not its own.
//
// It is a LEAF — stdlib only — so any app can serve its UI without linking the
// fleet.
package spa

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// Handler serves the built SPA rooted at root. Mount it under
// StripPrefix("/<app>", …) so it sees root-relative paths.
//
// name is the app the bundle belongs to and appears only in the 503 body, so an
// operator reading a failed deploy is told WHICH bundle is missing rather than
// that "the UI" is.
func Handler(root fs.FS, name string) http.Handler {
	files := http.FileServer(http.FS(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" {
			p = "index.html"
		}
		asset := p == assetDir || strings.HasPrefix(p, assetDir+"/")

		info, err := fs.Stat(root, p)
		switch {
		case err != nil || info.IsDir():
			// Not a file this bundle serves. Under assets/ that is a miss and
			// says so; anywhere else it is a client-side route.
			//
			// A miss under assets/ is 404 only once the bundle is BUILT. With no
			// shell to serve there is no stale chunk to miss — the bundle never
			// synced, and every path says that under the app's name rather than
			// reporting a plausible 404 for a deploy that did not happen.
			if asset && built(root) {
				http.NotFound(w, r)
				return
			}
			index(w, root, name)
			return
		case asset:
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		default:
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

// assetDir is the one content-addressed subtree: Vite hashes every filename under
// it, which is what makes both the immutable cache hint and the 404-on-miss
// correct there and nowhere else.
const assetDir = "assets"

// built reports whether a shell was synced into this bundle at all.
func built(root fs.FS) bool {
	_, err := fs.Stat(root, "index.html")
	return err == nil
}

// index writes the SPA shell, or says which bundle is missing.
func index(w http.ResponseWriter, root fs.FS, name string) {
	data, err := fs.ReadFile(root, "index.html")
	if err != nil {
		http.Error(w, name+" UI not built (see apps/"+name+"/ui/README.md)", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

// Sub is fs.Sub(embedded, "dist") without the error a caller cannot act on: an
// embed.FS is validated at COMPILE time, so a missing dist/ is a build failure,
// never a runtime branch. Every ui package spells the same two lines otherwise.
func Sub(embedded fs.FS) fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		return embedded
	}
	return sub
}
