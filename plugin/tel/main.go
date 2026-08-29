package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/tel"
)

// Standalone entry for the telecom app.
//
// Its own composition root: it links only this subsystem and the cloud request
// tier, never the whole fleet. The light host loads it as a plugin; run directly
// it serves standalone.
//
// Metered, not Free: ordering a number, sending a message and placing a call all
// buy from a telephony carrier at the carrier's price. The meter is
// apps/tel/meter.go, and it fires only when a carrier credential is held — a
// deployment running the in-process stub buys nothing and is billed nothing.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "tel",
		Price:    cloud.Metered,
		Use:      tel.Use,
		Shutdown: cloud.CtxShutdown(tel.Shutdown),
	}}, []string{"tel"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
