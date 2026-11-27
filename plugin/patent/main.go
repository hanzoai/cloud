package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud/apps/patent"
	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/sdk"
)

// Standalone entry for the patent app.
//
// This is the app's OWN composition root — it links only apps/patent and the
// cloud request tier, never package apps, so the build is this one subsystem
// and not the whole fleet. The light host loads it as a plugin; run directly it
// serves standalone. Its OpenAPI subset comes from `patent openapi`.
//
// Scaffolded by plugin/gen-app-cmds from the manifest.Apps row; now hand-owned —
// add a Shutdown/OwnsHealth/metered Price here if the app grows to need one.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "patent",
		Price:    cloud.Free,
		Use:      patent.Use,
		Shutdown: cloud.CtxShutdown(patent.Shutdown),
		// apps/patent registers /v1/patent/health in Mount.
		OwnsHealth: true,
	}}, []string{"patent"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
