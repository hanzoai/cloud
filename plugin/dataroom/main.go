package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/dataroom"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the dataroom app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `dataroom openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:       "dataroom",
		Price:      cloud.Free,
		Use:        dataroom.Use,
		Shutdown:   dataroom.Shutdown,
		OwnsHealth: true,
		// The trust centre's platform roster answers at /v1/admin/dataroom, which
		// UsePrefixes' /v1/<name> default does not cover — so without this the
		// scoped router owns one of the two subtrees the app serves, its middleware
		// lands outside the other, and cloud.Declare attributes that traffic to
		// nobody. The manifest is the one place those prefixes are written down.
		Prefixes: manifest.PrefixesFor("dataroom"),
	}}, []string{"dataroom"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
