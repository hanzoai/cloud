package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/venue"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the venue app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `venue openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Serve([]cloud.Plugin{{
		Name:  "venue",
		Price: cloud.Metered,
		Mount: venue.Mount,
		// The subsystem is named "venue" and serves /v1/cloud, so the /v1/<Name>
		// convention MountPrefixes falls back to covered NOTHING it registers:
		// undeclared, cloud.Declare attributed every /v1/cloud route to no
		// subsystem (the tracing and price lookups every request makes), and
		// scope.Use installed this app's middleware on /v1/venue, where no route
		// lives. That is load-bearing now, because cloud.Bridge is what parks the
		// validated org a TYPED op reads. Taken from the fleet's own routing table
		// so the two views cannot drift. Same shape as apps/plan and
		// apps/analytics; re-measure per app rather than assuming.
		Prefixes: manifest.PrefixesFor("venue"),
	}}, []string{"venue"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
