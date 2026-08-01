package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/product"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the product app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `product openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Serve([]cloud.Plugin{{
		Name:  "product",
		Price: cloud.Free,
		Mount: product.Mount,
		// This subsystem is named "product" and serves NEITHER "/v1/product" nor
		// anything under it — its four routes are /v1/search/{indexes,stats}
		// and /v1/vector/{collections,stats}. The /v1/<Name> convention
		// MountPrefixes assumes therefore covered NOTHING it registers: every
		// request here was attributed to no subsystem by the tracing and price
		// index cloud.Declare builds from this, and any middleware the subsystem
		// installed through the scoped Router landed on "/v1/product" and never
		// ran. The apps/plan defect, one app over. The list comes from the manifest
		// so it cannot drift from the prefixes the host routes here.
		Prefixes: manifest.PrefixesFor("product"),
	}}, []string{"product"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
