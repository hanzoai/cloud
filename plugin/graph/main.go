package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud/apps/graph"
	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk"
)

// Standalone entry for the graph app.
//
// This is the app's OWN composition root — it links only apps/graph and the
// cloud request tier, never package apps, so the build is this one subsystem and
// not the whole fleet. The light host loads it as a plugin; run directly it
// serves standalone. Its OpenAPI subset comes from `graph openapi`.
//
// Shutdown is NOT optional here. Every rollout tears this process down, and
// closing each tenant's assertion plane is what flushes it to its durable slot.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "graph",
		Prefixes: manifest.PrefixesFor("graph"),
		Price:    cloud.Free,
		Use:      graph.Use,
		Shutdown: cloud.CtxShutdown(graph.Shutdown),
	}}, []string{"graph"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
