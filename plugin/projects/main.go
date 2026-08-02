package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/projects"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the projects app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `projects openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "projects",
		Price: cloud.Metered,
		// This app answers on THREE subtrees, not the /v1/<name> convention, and it
		// installs the typed-op bridge + the money envelope on each of them. Read from
		// the manifest so the host's view of what projects serves and the app's own
		// view of what it may gate are one list, not two that can drift.
		Prefixes: manifest.PrefixesFor("projects"),
		Mount:    projects.Mount,
		Shutdown: cloud.CtxShutdown(projects.Shutdown),
	}}, []string{"projects"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
