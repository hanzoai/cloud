package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/entitlements"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the entitlements app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `entitlements openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "entitlements",
		Price:    cloud.Free,
		Mount:    entitlements.Mount,
		Shutdown: entitlements.Shutdown,
		// This subsystem owns TWO top-level nouns — /v1/entitlements (the commerce
		// projection) and /v1/orgs/:org/entitlements (the enablement store) — and the
		// /v1/<Name> default MountPrefixes falls back to covers only the first. Half
		// its surface was therefore attributed to NO subsystem by tracing and the
		// price index, and the middleware it installs through its own router —
		// including the typed-op Bridge that carries the validated org to every op —
		// was installed on /v1/entitlements alone and never ran for the org routes.
		// The apps/plan defect, in its partial form. The list comes from the manifest
		// so it cannot drift from the prefixes the host routes here.
		Prefixes: manifest.PrefixesFor("entitlements"),
	}}, []string{"entitlements"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
