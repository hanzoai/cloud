package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/plan"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the plan app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `plan openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Serve([]cloud.Plugin{{
		Name:  "plan",
		Price: cloud.Free,
		Mount: plan.Mount,
		// This subsystem is named "plan" and serves "/v1/plans", so the
		// /v1/<Name> convention MountPrefixes assumes is off by one letter and
		// covers nothing it registers. Undeclared, every /v1/plans request was
		// attributed to NO subsystem by the tracing and price index cloud.Declare
		// builds from this (ownerOf matches "/v1/plan" and "/v1/plan/…", never
		// "/v1/plans"), and the subsystem's own middleware — including the typed-op
		// Bridge that carries the validated org to every op — installed on
		// "/v1/plan" and never ran. The list comes from the manifest so it cannot
		// drift from the prefix the host routes here.
		Prefixes:   manifest.PrefixesFor("plan"),
		OwnsHealth: true,
	}}, []string{"plan"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
