package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud/apps/commerce"
	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/sdk"
)

// Standalone entry for the commerce app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `commerce openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "commerce",
		Price: cloud.Free,
		// commerce installs its identity boundary over all of /v1.
		Root: commerce.Use,
		// The liveness probe is commerce's own typed op (mount.go), registered
		// before the module embed boots so it still answers when the embed
		// failed and every business route serves the fail-closed 503. Declared
		// here so serve.go's generic always-ok /v1/commerce/health steps aside;
		// two declarations of one address is a boot refusal, not a merge.
		OwnsHealth: true,
	}}, []string{"commerce"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
