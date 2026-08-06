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
// the revision had to be prepared — fetched and indexed — which only the app
// knows. The edge therefore charges nothing and apps/lsp/meter.go owns the debit:
// a prepare is billed, a query against a prepared revision is not.
//
// There is no Shutdown. The language servers and the checkouts run in the
// hanzoai/lsp daemon on its own deployment, not here; this app holds an
// http.Client, which the process exiting reclaims.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "lsp",
		Price: cloud.Metered,
		Mount: lsp.Mount,
	}}, []string{"lsp"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
