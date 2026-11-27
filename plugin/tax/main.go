package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud/apps/tax"
	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/sdk"
)

// Standalone entry for the tax app.
//
// This is the app's OWN composition root — it links only apps/tax and the
// cloud request tier, never package apps, so the build is this one subsystem
// and not the whole fleet. The light host loads it as a plugin; run directly it
// serves standalone. Its OpenAPI subset comes from `tax openapi`.
//
// Metered, not Free: preparing and furnishing a 1099 are metered acts inside the
// app (apps/tax charge, kinds "prepare" and "furnish"). Billing's catalog prices
// neither, so both are metered at zero until it does; nothing in the environment
// changes the price.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "tax",
		Price:    cloud.Metered,
		Use:      tax.Use,
		Shutdown: cloud.CtxShutdown(tax.Shutdown),
	}}, []string{"tax"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
