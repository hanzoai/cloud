package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/mq"
)

// Standalone entry for the mq app.
//
// This is the app's OWN composition root — it links only apps/mq and the
// cloud request tier, never package apps, so the build is this one subsystem
// and not the whole fleet. The light host loads it as a plugin; run directly it
// serves standalone. Its OpenAPI subset comes from `mq openapi`.
//
// OwnsHealth: the app serves its own typed /v1/mq/health — the broker's
// answer, not the generic liveness stub. Shutdown drains the broker client.
func main() {
	if err := cloud.Serve([]cloud.Plugin{{
		Name:       "mq",
		Price:      cloud.Free,
		Mount:      mq.Mount,
		Shutdown:   mq.Shutdown,
		OwnsHealth: true,
	}}, []string{"mq"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
