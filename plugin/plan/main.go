package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/plan"
)

// Standalone entry for the plan app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `plan openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:       "plan",
		Price:      cloud.Free,
		Mount:      plan.Mount,
		OwnsHealth: true,
	}}, []string{"plan"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
