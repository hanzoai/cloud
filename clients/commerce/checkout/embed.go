// Package checkout: this file embeds the Vite-built SPA into the commerce
// binary. The source lives under ui/ and builds into ui/dist via the
// checkout-build Dockerfile stage. Local Go test runs only need the
// .gitkeep in ui/dist/ so the go:embed directive resolves.
package checkout

import (
	"embed"
	"io/fs"
)

// UIFS is the embedded checkout SPA bundle. Mirror of admin/embed.go.
//
//go:embed all:ui/dist
var UIFS embed.FS

// UISub returns the dist/ subtree as an fs.FS — ready for http.FS.
func UISub() fs.FS {
	sub, err := fs.Sub(UIFS, "ui/dist")
	if err != nil {
		return embed.FS{}
	}
	return sub
}
