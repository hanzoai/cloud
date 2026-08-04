package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/label"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the label app.
//
// This is the app's OWN composition root — it links only apps/label and the
// cloud request tier, never package apps, so the build is this one subsystem
// and not the whole fleet. The light host loads it as a plugin; run directly it
// serves standalone. Its OpenAPI subset comes from `label openapi`.
//
// Scaffolded by plugin/gen-app-cmds from the manifest.Apps row; now hand-owned.
//
// Shutdown is NOT optional here. cloud deploys strategy Recreate at one replica,
// so every rollout tears this process down, and closing each tenant's record
// plane is what flushes it to its durable slot. A ground-truth record that only
// existed in a pod is a compliance record that did not exist.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "label",
		Prefixes: manifest.PrefixesFor("label"),
		Price:    cloud.Free,
		Mount:    label.Mount,
		Shutdown: cloud.CtxShutdown(label.Shutdown),
	}}, []string{"label"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
