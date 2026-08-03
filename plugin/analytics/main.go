package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/analytics"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the analytics app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `analytics openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "analytics",
		Price: cloud.Free,
		Mount: analytics.Mount,
		// The SIX paths this app answers — the read lenses AND the ingest doors —
		// taken from the fleet's own routing table so the two views cannot drift.
		// Undeclared, MountPrefixes fell back to the /v1/<name> convention and five
		// of the six were outside every prefix this subsystem owned: cloud.Declare's
		// index attributed /v1/errors, /v1/insights/* and /v1/event to NO subsystem
		// (the tracing and price lookups every request makes), and scope.Use could
		// not install this app's own middleware on them — which is now load-bearing,
		// because cloud.Bridge is what parks the validated org a TYPED op reads.
		Prefixes:   manifest.PrefixesFor("analytics"),
		OwnsHealth: true,
		// Analytics owns two background attachments to the bus — the event sink's
		// consumers and the ingest connection — so it has a shutdown to run. Without
		// this the drain outlived the process's own teardown and an in-flight insert
		// could be cut off mid-commit.
		Shutdown: analytics.Shutdown,
	}}, []string{"analytics"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
