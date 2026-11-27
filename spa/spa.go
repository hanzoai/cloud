// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

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
				w.Header().Set("Cache-Control", "no-store, max-age=0")
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
		if ct := pinnedMIME(path.Ext(p)); ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		files.ServeHTTP(w, r)
	})
}

// pinnedMIME pins standard web MIME types so minimal container images (where
// /etc/mime.types may omit .mjs/.cjs/.map or mis-type scripts) never trigger
// strict MIME checking failures under X-Content-Type-Options: nosniff.
func pinnedMIME(ext string) string {
	switch strings.ToLower(ext) {
	case ".js", ".mjs", ".cjs":
		return "text/javascript; charset=utf-8"
	case ".css":
		return "text/css; charset=utf-8"
	case ".wasm":
		return "application/wasm"
	case ".json", ".map":
		return "application/json; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	default:
		return ""
	}
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
