// cloud is the unified Hanzo Cloud binary per HIP-0106.
//
// One binary. Many subsystems. Deployment configuration determines
// which subsystems mount at startup. Same artifact powers
// api.hanzo.ai, api.osage.cloud, api.lux.cloud, api.zoo.cloud, and
// every other white-label resold cloud surface.
//
// The serve body lives in cloud.Serve (one place, shared with the `hanzo`
// subcommand dispatcher); main() is just its full-surface entrypoint.
package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"

	// Subsystems — each ships func Mount(*zip.App, cloud.Deps) error and
	// registers itself via init() in cloud.Registry. Import paths reflect
	// where each subsystem's Mount lives. Blank-importing them here populates
	// the registry that cloud.Serve mounts over.
	_ "github.com/hanzoai/ai"          // order 150
	_ "github.com/hanzoai/amqp"        // order 30
	_ "github.com/hanzoai/authz"       // order 70
	_ "github.com/hanzoai/base"        // order 60
	_ "github.com/hanzoai/commerce"    // order 100
	_ "github.com/hanzoai/gateway"     // order 80
	_ "github.com/hanzoai/iam/pkg/iam" // order 50 (Mount lives in pkg/iam submodule)
	_ "github.com/hanzoai/ingress"     // order 90
	_ "github.com/hanzoai/kms"         // order 10
	_ "github.com/hanzoai/licensing"   // order 110 (after iam + commerce)
	_ "github.com/hanzoai/mcp/go"      // order 160 (Mount lives in go submodule)
	_ "github.com/hanzoai/metrics"     // order 40 (native ZAP-native metrics store)
	_ "github.com/hanzoai/o11y"        // order 70 (mounts alongside authz)
	_ "github.com/hanzoai/vfs"         // order 20

	// Node-service subsystems hosted in-process via base+goja (HIP-0106).
	// Each loads its service repo's goja/bundle.js into a goja runtime and
	// registers /v1/* routes. The JS + catalog data live in the service repos
	// (hanzoai/plans, hanzoai/pricing); these wrappers are glue in cloud.
	_ "github.com/hanzoai/cloud/clients/plansvc"    // order 111 — /v1/plans/*
	_ "github.com/hanzoai/cloud/clients/pricingsvc" // order 112 — /v1/pricing/*
)

func main() {
	// nil ⇒ honor cfg.Enable from flags/env (empty = all subsystems).
	if err := cloud.Serve(nil); err != nil {
		fmt.Fprintf(os.Stderr, "cloud: %v\n", err)
		os.Exit(1)
	}
}
