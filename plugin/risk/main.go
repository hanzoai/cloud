package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/risk"
	"github.com/hanzoai/cloud/manifest"
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
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "risk",
		Price: cloud.Metered,
		Mount: risk.Mount,
		// Declared, and taken from the fleet's own routing table so the two views
		// cannot drift. This app answers on TWO prefixes — /v1/risk and the native
		// leaves of /v1/ml — and undeclared, MountPrefixes falls back to the
		// /v1/<name> convention, which puts every ml leaf outside the prefix this
		// subsystem owns: the index attributes them to nobody and scope.Use cannot
		// install this app's own middleware on them.
		Prefixes:   manifest.PrefixesFor("risk"),
		Shutdown:   risk.Shutdown,
		OwnsHealth: true,
	}}, []string{"risk"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
