package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/git"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the git app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `git openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name: "git",
		// git answers TWO roots — the control surface at /v1/git and the runner
		// daemon's wire at /v1/runner — so it states them rather than taking the
		// one the default derives from its name. Read from the manifest, which is
		// the fleet's source for the same fact.
		Prefixes: manifest.PrefixesFor("git"),
		Price:    cloud.Free,
		Use:      git.Use,
		Shutdown: cloud.CtxShutdown(git.Shutdown),
	}}, []string{"git"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
