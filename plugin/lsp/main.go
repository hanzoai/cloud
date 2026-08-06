package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/lsp"
)

// Standalone entry for the lsp app.
//
// This is the app's OWN composition root — it links only apps/lsp and the
// cloud request tier, never package apps, so the build is this one subsystem
// and not the whole fleet. The light host loads it as a plugin; run directly it
// serves standalone. Its OpenAPI subset comes from `lsp openapi`.
//
// Scaffolded by plugin/gen-app-cmds from the manifest.Apps row; now hand-owned.
//
// Metered, not Free: a query's cost is not a property of the door but of whether
// it had to check the repository out and index it, which only the app knows. The
// edge therefore charges nothing and apps/lsp/meter.go owns the debit — a cold
// start is billed, a warm point query is not.
//
// Shutdown is not optional here. A warm workspace is a LIVE language-server
// subprocess and a checkout on disk; neither is reclaimed by this process exiting,
// so without the hook a rolling deploy leaves orphaned gopls processes behind.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "lsp",
		Price:    cloud.Metered,
		Mount:    lsp.Mount,
		Shutdown: lsp.Shutdown,
	}}, []string{"lsp"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
