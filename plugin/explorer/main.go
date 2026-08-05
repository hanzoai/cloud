package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/explorer"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the explorer app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `explorer openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name: "explorer",
		// Declared: undeclared falls back to /v1/explorer, which this app does not
		// serve — the scope then guards a path with no routes and zip refuses to
		// compose (see plugin/account/main.go).
		Prefixes: manifest.PrefixesFor("explorer"),
		Price:    cloud.Free,
		Mount:    explorer.Mount,
	}}, []string{"explorer"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
