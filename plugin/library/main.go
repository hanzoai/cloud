package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud/apps/library"
	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/sdk"
)

// Standalone entry for the library app.
//
// This is the app's OWN composition root — it links only apps/library and the
// cloud request tier, never package apps, so the build is this one subsystem
// and not the whole fleet. The light host loads it as a plugin; run directly it
// serves standalone. Its OpenAPI subset comes from `library openapi`.
//
// Scaffolded by plugin/gen-app-cmds from the manifest.Apps row; now hand-owned —
// add a Shutdown/OwnsHealth/metered Price here if the app grows to need one.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "library",
		Price:    cloud.Free,
		Use:      library.Use,
		Shutdown: library.Shutdown,
	}}, []string{"library"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
