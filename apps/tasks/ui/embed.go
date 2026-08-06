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
// EmbeddedTasks engine + apps/tasks's /v1/tasks surface), so cloud owns the
// UI embed too: one binary, one origin, the real UI. This retires the standalone
// tasks-ui pod (a Temporal-Web-UI fork).
//
// dist/ is the committed, content-addressed Vite build. The SPA is built with
// base '/tasks/' and API prefix '/v1/tasks' (see the admin-tasks vite.config),
// so every asset + XHR is same-origin under the paths cloud already serves. To
// refresh it, rebuild the admin-tasks app and sync its dist/ here — see
// apps/tasks/ui/README.md.
package ui

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/hanzoai/cloud/spa"
)

// dist WITHOUT `all:` — the prefix that would also embed dot-files. dist/.sync-stamp
// is provenance for whoever regenerates this bundle (source repo, branch, commit):
// a fact for the repo, not a file to publish. `all:dist` embedded it and the SPA
// handler serves anything it can stat, so the build's origin was readable by anyone
// who guessed the path. Plain `dist` omits every `.`-prefixed entry.
//
//go:embed dist
var distFS embed.FS

// FS returns the embedded built-UI filesystem rooted at dist/.
func FS() fs.FS { return spa.Sub(distFS) }

// Handler serves the embedded SPA. Mount it under StripPrefix("/tasks", …) so it
// sees root-relative paths. The serving policy — immutable hashed assets, index
// fallback for a deep link, 503 for an unsynced bundle — is spa.Handler's, which
// is the ONE copy of it in this repo.
func Handler() http.Handler { return spa.Handler(FS(), "tasks") }
