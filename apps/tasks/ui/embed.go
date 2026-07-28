// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package ui embeds the built Hanzo Tasks SPA (@hanzo/tasks, the admin-tasks
// app in hanzoai/admin, Vite + hanzogui) directly into the cloud binary and
// serves it at /tasks/* (console.hanzo.ai/tasks + tasks.hanzo.ai/tasks).
//
// WHY cloud owns this embed (not github.com/hanzoai/tasks/ui): the tasks module
// ships an EMPTY ui/dist placeholder ("No UI build present") because its bundle
// is produced in a separate frontend workspace and synced in at release time —
// a step the tasks module's own releases do not run, so cloud's tasks UI was a
// placeholder. cloud is the ONE process that serves tasks.hanzo.ai (durable.go's
// EmbeddedTasks engine + clients/tasks's /v1/tasks surface), so cloud owns the
// UI embed too: one binary, one origin, the real UI. This retires the standalone
// tasks-ui pod (a Temporal-Web-UI fork).
//
// dist/ is the committed, content-addressed Vite build. The SPA is built with
// base '/tasks/' and API prefix '/v1/tasks' (see the admin-tasks vite.config),
// so every asset + XHR is same-origin under the paths cloud already serves. To
// refresh it, rebuild the admin-tasks app and sync its dist/ here — see
// clients/tasks/ui/README.md.
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
		// Impossible at runtime: embed.FS entries are validated at compile
		// time. If dist/ were missing the binary would carry an empty FS.
		return distFS
	}
	return sub
}

// Handler returns an http.Handler that serves the embedded SPA. Mount it under
// StripPrefix("/tasks", …) so it sees root-relative paths.
//
//   - Content-addressed assets under assets/ ship immutable cache hints (Vite
//     hashes filenames).
//   - Any path that is not a real file rewrites to index.html so the client-side
//     router handles the route — the standard SPA fallback that lets a deep-link
//     reload survive.
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

// serveIndex writes index.html with no-cache so a freshly-deployed build
// replaces the stale shell on the next request.
func serveIndex(w http.ResponseWriter, r *http.Request, root fs.FS) {
	data, err := fs.ReadFile(root, "index.html")
	if err != nil {
		http.Error(w, "tasks UI not built (see clients/tasks/ui/README.md)", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
	_ = r
}
