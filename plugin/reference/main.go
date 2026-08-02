package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/reference"
)

// Standalone entry for the reference app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the union the fused binary was. The light host loads it as a plugin;
// run directly it serves standalone. Its OpenAPI subset comes from
// `reference openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Serve([]cloud.Plugin{{
		Name:     "reference",
		Price:    cloud.Free,
		Mount:    reference.Mount,
		Shutdown: cloud.CtxShutdown(reference.Shutdown),
	}}, []string{"reference"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
