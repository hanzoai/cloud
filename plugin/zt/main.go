package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/zt"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the zt app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `zt openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "zt",
		Price: cloud.Free,
		Mount: zt.Mount,
		// This subsystem is named "zt" and serves NEITHER "/v1/zt" nor anything
		// under it — its four routes are /v1/networks[/routers|/:id] and
		// /v1/mesh/services. The /v1/<Name> convention MountPrefixes assumes therefore
		// covered NOTHING it registers, so every request here was attributed to no
		// subsystem and any middleware installed through the scoped Router landed
		// on "/v1/zt" and never ran. The apps/plan defect, one app over. The list
		// comes from the manifest so it cannot drift from the prefixes the host
		// routes here.
		Prefixes: manifest.PrefixesFor("zt"),
	}}, []string{"zt"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
