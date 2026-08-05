// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Package ui embeds the built Hanzo Meet SPA — the native video-call client —
// into the cloud binary and serves it at /meet/*.
//
// It replaces the office plugin in the published Team front, which was the last
// part of a Hanzo call that was not ours: the media server (LiveKit at
// wss://live.hanzo.bot) and the token that opens it (apps/meet) were already
// native, so a forked front shipping a whole product's UI was carrying one screen.
//
// The call itself is LiveKit's own React components — @livekit/components-react
// over livekit-client — because a hand-rolled WebRTC client is a second
// implementation of a protocol whose reference implementation the media server
// already ships against. The chrome around it is @hanzogui, like every other
// Hanzo surface.
//
// dist/ is the committed, content-addressed Vite build, produced with
// base '/meet/' and VITE_API_PREFIX=/v1/meet, so every asset and every XHR is
// same-origin under paths this binary already serves. Do NOT hand-edit it — see
// README.md to regenerate.
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

// Handler serves the embedded SPA. Mount it under StripPrefix("/meet", …) so it
// sees root-relative paths. The serving policy — immutable hashed assets, index
// fallback for a deep link, 503 for an unsynced bundle — is spa.Handler's, which
// is the ONE copy of it in this repo.
func Handler() http.Handler { return spa.Handler(FS(), "meet") }
