package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud/apps/reward"
	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk"
)

// Standalone entry for the reward app.
//
// This is the app's OWN composition root: it links only apps/reward and the cloud
// request tier, never package apps, so the build is this one subsystem and not the
// whole fleet. The light host loads it as a plugin; run directly it serves
// standalone. Its OpenAPI subset comes from `reward openapi`. Hand-owned.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name: "reward",
		// The module serves the program at /v1/reward and its staff queue at
		// /v1/admin/reward; undeclared, the scope guards only /v1/reward.
		Prefixes: manifest.PrefixesFor("reward"),
		Price:    cloud.Free,
		Use:      reward.Use,
		Shutdown: reward.Shutdown,
		// The module answers GET /v1/reward/health itself, from its own store.
		OwnsHealth: true,
	}}, []string{"reward"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
