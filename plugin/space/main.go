package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/space"
)

// Standalone entry for the space app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the fleet union. The light host loads it as a plugin; run directly it
// serves standalone. Its OpenAPI subset comes from `space openapi`.
//
// Metered, and OwnsHealth: every operation but the probe takes a per-operation
// fee out of the caller's ledger, and /v1/space/health is a REAL fail-closed
// probe of the object store — so Serve's generic always-ok liveness route must
// not shadow it with a fake 200.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:       "space",
		Price:      cloud.Metered,
		Use:        space.Use,
		OwnsHealth: true,
	}}, []string{"space"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
