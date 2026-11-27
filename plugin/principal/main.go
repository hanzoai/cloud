package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud/apps/principals"
	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/sdk"
)

// Standalone entry for the principal app.
//
// This is the app's OWN composition root — it links only apps/principals and the
// cloud request tier, never package apps, so the build is this one subsystem
// and not the whole fleet. The light host loads it as a plugin; run directly it
// serves standalone. Its OpenAPI subset comes from `principal openapi`.
//
// Free: composing a principal, screening it, clearing a payment and attesting an
// agent charge nothing.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "principal",
		Price:    cloud.Free,
		Use:      principals.Use,
		Shutdown: cloud.CtxShutdown(principals.Shutdown),
	}}, []string{"principal"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
