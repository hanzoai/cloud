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

	"github.com/hanzoai/cloud/spa"
)

//go:embed all:dist
var distFS embed.FS

// FS returns the embedded built-UI filesystem rooted at dist/.
func FS() fs.FS { return spa.Sub(distFS) }

// Handler serves the embedded board. Mount it under StripPrefix("/research", …)
// so it sees root-relative paths. The serving policy is spa.Handler's — the ONE
// copy of it in this repo.
func Handler() http.Handler { return spa.Handler(FS(), "research") }
