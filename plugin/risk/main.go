package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/risk"
)

// Standalone entry for the risk app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the union the fused binary was. The light host loads it as a plugin;
// run directly it serves standalone. Its OpenAPI subset comes from
// `risk openapi`.
//
// Price is cloud.Metered and not a flat edge price. Every priced act here bills
// from the ResourceMeter on the unit it actually produced — one screen per
// decision, one run per search — and a flat per-request edge price would charge
// a second time for the same work.
//
// Shutdown is not optional for this app. The model is in-memory mutable state
// and cloud deploys strategy Recreate at one replica, so a rollout that did not
// snapshot would return every tenant to warming — and a warming model refuses to
// score, which reads as "clean" to anything that does not check the refusal.
func main() {
	if err := cloud.Serve([]cloud.Plugin{{
		Name:       "risk",
		Price:      cloud.Metered,
		Mount:      risk.Mount,
		Shutdown:   risk.Shutdown,
		OwnsHealth: true,
	}}, []string{"risk"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
