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

// Handler serves the embedded board. Mount it under StripPrefix("/research", …)
// so it sees root-relative paths. The serving policy is spa.Handler's — the ONE
// copy of it in this repo.
func Handler() http.Handler { return spa.Handler(FS(), "research") }
