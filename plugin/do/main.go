package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/do"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the do app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `do openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name: "do",
		// Declared: undeclared falls back to /v1/do, which this app does not
		// serve — the scope then guards a path with no routes and zip refuses to
		// compose (see plugin/account/main.go).
		Prefixes: manifest.PrefixesFor("do"),
		Price:    cloud.Free,
		Mount:    do.Mount,
	}}, []string{"do"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
