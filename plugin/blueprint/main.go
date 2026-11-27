package main

import (
	"fmt"
	"net/http"
	"os"

	luxlog "github.com/luxfi/log"

	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/hanzoai/cloud/apps/blueprint"
)

// The sbom read's prose, declared HERE because blueprint is built from another
// module and imports zip, never cloud, so it cannot reach spec.Describe — and the
// route is untyped (one address answers two shapes at 200), which leaves zipdoc
// nothing to lift. Describe is additive metadata on a route that EXISTS: a
// description whose route is not served never renders, so this cannot invent an
// operation.
func init() {
	spec.Describe("/v1/blueprint/sbom", http.MethodGet,
		"A blueprint's bill of images and what running it costs",
		"Answers a blueprint's SBOM — the container images its compose stack runs, each with the "+
			"CPU/memory footprint that was applied to it — together with the compute cost that "+
			"footprint prices out to on the active rate card.\n\n"+
			"ONE address, TWO shapes at 200: `?template=<id>` returns that blueprint's Estimate "+
			"alone (404 on an id no embedded blueprint carries), and no `template` returns "+
			"`{data:[Estimate]}` for every blueprint — the batch the console's template gallery "+
			"reads in one round-trip.\n\n"+
			"The blueprints are reference content embedded in the binary and validated at mount, "+
			"so this read is the same for every caller and is scoped to no tenant. The per-hour "+
			"figure it returns is the one the deploy path meters the deploying org on and the 20% "+
			"author royalty is taken from; GET /v1/blueprint/health echoes the rate card it was "+
			"priced from.")
}

// Standalone entry for the blueprint app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `blueprint openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:       "blueprint",
		Price:      cloud.Free,
		OwnsHealth: true,
		// The adapter lives HERE, on cloud's side: blueprint is built from
		// hanzoai/cloud and must never import this host, so the host's plugin
		// contract bends to the app rather than the app learning about Deps.
		Use: func(app cloud.Router, deps cloud.Deps) error {
			return blueprint.Mount(app, luxlog.Default().New("subsystem", "blueprint"))
		},
	}}, []string{"blueprint"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
