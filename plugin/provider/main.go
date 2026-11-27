package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud/apps/provider"
	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk"
)

// Standalone entry for the provider app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `provider openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name: "provider",
		// The paths this app answers, read from the fleet's one list rather than
		// restated here (manifest/one_source_test.go). Two prefixes: the org
		// surface at the app's name, and the per-user connector plane at
		// /v1/connection. Each opens its own group and carries its own middleware.
		Prefixes: manifest.PrefixesFor("provider"),
		Price:    cloud.Free,
		Use:      provider.Use,
		Shutdown: provider.Shutdown,
	}}, []string{"provider"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
