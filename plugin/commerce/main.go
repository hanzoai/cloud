package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/commerce"
)

// Standalone entry for the commerce app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `commerce openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "commerce",
		Price: cloud.Free,
		Mount: commerce.Mount,
		// commerce wraps ALL of /v1 (mount.go: app.Group("/v1").Use(...)), so the
		// grant is real and stated, not inherited from a signature.
		Global: true,
	}}, []string{"commerce"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
