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
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "tel",
		Price:    cloud.Free,
		Mount:    tel.Mount,
		Shutdown: cloud.CtxShutdown(tel.Shutdown),
	}}, []string{"tel"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
