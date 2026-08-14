package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/taxonomy"
)

// Standalone entry for the taxonomy app.
//
// This is the app's OWN composition root — it links only apps/taxonomy and the
// cloud request tier, never package apps, so the build is this one subsystem
// and not the whole fleet. The light host loads it as a plugin; run directly it
// serves standalone. Its OpenAPI subset comes from `taxonomy openapi`.
//
// Scaffolded by plugin/gen-app-cmds from the manifest.Apps row; now hand-owned —
// add a Shutdown/OwnsHealth/metered Price here if the app grows to need one.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "taxonomy",
		Price:    cloud.Free,
		Mount:    taxonomy.Mount,
		Shutdown: taxonomy.Shutdown,
	}}, []string{"taxonomy"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
