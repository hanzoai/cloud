// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package ui embeds the built Hanzo Todo SPA (@hanzo/tracker, the tracker
// app in hanzoai/admin, Vite + hanzogui shell over the forge's own board CSS)
// into the cloud binary and serves it at /tracker/*.
//
// WHY cloud owns this embed: cloud is the ONE process that answers
// /v1/tracker (apps/tracker's per-org board store), so it serves the UI for
// that store too — one binary, one origin, one deploy.
//
// WHAT THE PAGE ASSERTS, and what it merely asks for. The SPA signs in at
// hanzo.id and holds the bearer; it never asserts an org. It does SELECT one,
// as X-Org-Id, and that is a request rather than a claim: SanitizeIdentity
// deletes the header on ingress and re-mints it from the validated token,
// honouring the selection only when the token's signed `orgs` membership claim
// contains it and falling back to the caller's home org otherwise. So the
// tenant key a handler reads is still minted here and never taken from the
// client — which is what lets the board offer an org switcher without the
// client-supplied tenancy the tracker refuses.
//
// This is what lets tracker.hanzo.ai serve a native board and retire the Huly
// tracker that answered that host — a front that ran its own login form, and
// so was a second place identity could be established on a host we own.
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

	"github.com/hanzoai/cloud/spa"
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
func FS() fs.FS { return spa.Sub(distFS) }

// Handler serves the embedded board. Mount it under StripPrefix("/tracker", …)
// so it sees root-relative paths. The serving policy — immutable hashed assets,
// 404 for a purged chunk, index fallback for a deep link, 503 for an unsynced
// bundle — is spa.Handler's, which is the ONE copy of it in this repo.
func Handler() http.Handler { return spa.Handler(FS(), "tracker") }
