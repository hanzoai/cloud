package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/licensing"
)

// Standalone entry for the licensing app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `licensing openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "licensing",
		Price: cloud.Free,
		// licensing is an external leaf whose Mount takes the concrete *zip.App.
		// The adapter lives HERE, on cloud's side, for the reason authz's does: the
		// plugin contract bends to the leaf, never the fleet's one signature.
		Mount:  func(app cloud.Router, deps cloud.Deps) error { return licensing.Mount(cloud.ZipApp(app), deps) },
		Global: true,
	}}, []string{"licensing"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
