package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud/apps/triage"
	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk"
)

// Standalone entry for the triage app: its own composition root, linking only
// apps/triage and the cloud request tier.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "triage",
		Prefixes: manifest.PrefixesFor("triage"),
		Price:    cloud.Free,
		Use:      triage.Use,
		Shutdown: triage.Shutdown,
	}}, []string{"triage"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
