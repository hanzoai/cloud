package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/network"
)

// Standalone entry for the network app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `network openapi`. Hand-owned — edit the spec below directly.
//
// IT DECLARES NO Prefixes, and that is what the rename bought. Named "zt" it
// served neither /v1/zt nor anything under it, so the /v1/<Name> convention
// MountPrefixes assumes covered NOTHING it registers: every request here was
// attributed to no subsystem and any middleware installed through the scoped
// Router landed on "/v1/zt" and never ran. The workaround was to restate the
// manifest's routing table here. Named for the address it serves, the convention
// is the truth again and there is nothing to restate.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "network",
		Price: cloud.Free,
		Mount: network.Mount,
	}}, []string{"network"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
