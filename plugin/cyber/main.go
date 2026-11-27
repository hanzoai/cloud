package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud/apps/cyber"
	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/sdk"
)

// Standalone entry for the cyber app.
//
// This is the app's OWN composition root: it links only apps/cyber and the
// cloud request tier, never the whole fleet. The light host loads it as a
// plugin; run directly it serves standalone.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:       "cyber",
		Use:        cyber.Use,
		Shutdown:   cloud.CtxShutdown(cyber.Shutdown),
		OwnsHealth: true,
		Price:      cloud.Free,
	}}, []string{"cyber"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
