package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud/apps/dataroom"
	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk"
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
		Prefixes:   manifest.PrefixesFor("dataroom"),
	}}, []string{"dataroom"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
