package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/ci"
)

// Standalone entry for the ci app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier. The light host loads it as a plugin; run directly it
// serves standalone.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "ci",
		Price:    cloud.Free,
		Use:      ci.Use,
		Shutdown: cloud.CtxShutdown(ci.Shutdown),
	}}, []string{"ci"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
