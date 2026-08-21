package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/trust"
)

// Standalone entry for the trust app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet. The light host loads it as a
// plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `trust openapi`.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "trust",
		Price:    cloud.Free,
		Mount:    trust.Mount,
		Shutdown: trust.Shutdown,
	}}, []string{"trust"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
