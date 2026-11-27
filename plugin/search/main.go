package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud/apps/search"
	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk"
)

// Standalone entry for the search app — the org-scoped hybrid query surface at
// POST /v1/search. Its own composition root: it links its own subsystem and the
// cloud request tier, never the whole fleet. The light host loads it as a
// plugin; run directly it serves standalone.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "search",
		Price:    cloud.Free,
		Use:      search.Use,
		Prefixes: manifest.PrefixesFor("search"),
	}}, []string{"search"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
