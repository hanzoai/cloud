package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/apps/pricing"
)

// Standalone entry for the pricing app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `pricing openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "pricing",
		Price: cloud.Free,
		Mount: pricing.Mount,
		// This surface answers FIVE subtrees, not the one the /v1/<name>
		// convention assumes — the catalog read plane, the self-service
		// enablement plane and the two admin planes over the same overlay store.
		// Undeclared, four of them were attributed to another subsystem (or to
		// none) by the tracing and price index cloud.Declare builds from this, and
		// the subsystem could not install its own middleware on them. The list is
		// the app's, so it cannot drift from the routes it registers.
		Prefixes:   manifest.PrefixesFor("pricing"),
		OwnsHealth: true,
	}}, []string{"pricing"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
