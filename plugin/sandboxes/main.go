package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/sandbox"
)

// Standalone entry for the sandboxes app.
//
// This is the app's OWN composition root — it links only apps/sandbox and the
// cloud request tier, never package apps, so the build is this one subsystem
// and not the whole fleet. The light host loads it as a plugin; run directly it
// serves standalone. Its OpenAPI subset comes from `sandboxes openapi`.
//
// Scaffolded by plugin/gen-app-cmds from the manifest.Apps row; now hand-owned —
// add a Shutdown/OwnsHealth/metered Price here if the app grows to need one.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "sandboxes",
		Price: cloud.Free,
		Mount: sandbox.Mount,
	}}, []string{"sandboxes"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
