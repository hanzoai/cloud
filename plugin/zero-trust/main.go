package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/zt"
)

// Standalone entry for the zero-trust app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `zero-trust openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Serve([]cloud.Plugin{{
		Name:  "zero-trust",
		Price: cloud.Free,
		Mount: zt.Mount,
	}}, []string{"zero-trust"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
