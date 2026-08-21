package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/validators"
)

// Standalone entry for the validators app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `validators openapi`. Hand-owned — edit the spec below directly.
// Metered, not Free: claiming a slot materializes a validator node on our cluster
// with 200Gi attached, running until it is deleted. Holding the NFT makes a caller
// ELIGIBLE and bounds the count to the token supply — which is why this was a
// revenue leak and not a denial-of-service one — but eligibility is not
// settlement. The meter is apps/validators/meter.go, and it charges the
// MATERIALIZATION: a claim that stays pending starts nothing and bills nothing.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "validators",
		Price:    cloud.Metered,
		Mount:    validators.Mount,
		Shutdown: cloud.CtxShutdown(validators.Shutdown),
	}}, []string{"validators"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
