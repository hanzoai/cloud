package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/market"
)

// Standalone entry for the market app.
//
// This is the app's OWN composition root — it links only apps/market and the cloud
// request tier, never package apps, so the build is this one subsystem and not the
// whole fleet. The light host loads it as a plugin; run directly it serves
// standalone. Its OpenAPI subset comes from `market describe`.
//
// No Shutdown: the app holds no store, no listener and no loop, so there is nothing
// to release that process exit does not.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "market",
		Price: cloud.Free,
		Use:   market.Use,
	}}, []string{"market"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
