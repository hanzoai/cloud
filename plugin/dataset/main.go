package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/dataset"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the dataset app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `dataset describe`.
//
// Metered, not Free: a materialisation is a bounded scan of a warehouse every
// other tenant shares, so admission to one is a priced act. The reads around it
// cost nothing and declare nothing — Price is what ONE request to this surface
// costs at the edge gate, and the per-op fee is stated in the app.
//
// It does not own health. Serve's generic /v1/dataset/health liveness route is
// the honest answer for this plane: everything it knows is in the store, so a
// probe of its own would report the store's reachability, which is already what
// every op reports in band as a 503 rather than as an empty answer.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name: "dataset",
		// Declared: undeclared falls back to /v1/dataset, which this app does not
		// serve — the scope then guards a path with no routes and zip refuses to
		// compose (see plugin/account/main.go).
		Prefixes: manifest.PrefixesFor("dataset"),
		Price:    cloud.Metered,
		Mount:    dataset.Mount,
	}}, []string{"dataset"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
