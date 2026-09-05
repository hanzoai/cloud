package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/tasks"
)

// Standalone entry for the tasks app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `tasks openapi` — this binary's OWN live router, written by `make -C
// apps/tasks openapi`; there is no spec in this file to edit.
//
// Scaffolded by plugin/gen-app-cmds from the manifest.Apps row; now hand-owned —
// add a Shutdown/OwnsHealth/metered Price here if the app grows to need one.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "tasks",
		Price: cloud.Free,
		Use:   tasks.Use,
	}}, []string{"task"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
