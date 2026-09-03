package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/integrations"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the integrations app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `integrations openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name: "integrations",
		// The paths this app answers, read from the fleet's one list rather than
		// restated here (manifest/one_source_test.go). It reads as the /v1/<name>
		// convention today because the whole surface is under that name now — the
		// user-plane connectors were a second top-level prefix, and left unsaid
		// they sat outside what the app claimed, so its own middleware skipped
		// them while the host kept forwarding them: served, and served wrong.
		Prefixes: manifest.PrefixesFor("integrations"),
		Price:    cloud.Free,
		Use:      integrations.Use,
		Shutdown: integrations.Shutdown,
	}}, []string{"integrations"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
