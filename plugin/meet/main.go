package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/meet"
)

// Standalone entry for the meet app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `meet openapi`. Hand-owned — edit the spec below directly.
//
// Metered, not Free: minting a join token is what admits a participant to the
// media server, and a participant is a live audio/video pipe for as long as they
// stay. The lobby, the health probe and the SPA beside it stay free — they are
// reads. The meter is apps/meet/meter.go, and the unit is the seat, because a
// seat is what this process can observe: media rides browser-to-SFU directly and
// no duration ever reaches this binary.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:       "meet",
		Price:      cloud.Metered,
		Use:        meet.Use,
		OwnsHealth: true,
	}}, []string{"meet"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
