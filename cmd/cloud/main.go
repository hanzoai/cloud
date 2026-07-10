// cloud is the unified Hanzo Cloud binary per HIP-0106.
//
// One binary. Many subsystems. Deployment configuration determines
// which subsystems mount at startup. Same artifact powers
// api.hanzo.ai, api.osage.cloud, api.lux.cloud, api.zoo.cloud, and
// every other white-label resold cloud surface.
//
// The serve body lives in cloud.Serve (one place, shared with the `hanzo`
// subcommand dispatcher); main() is just its full-surface entrypoint. The
// subsystem set is defined once in the subsystems bundle.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/hanzoai/cloud"

	// The subsystem set is defined ONCE in the subsystems bundle (shared with
	// cmd/hanzo). subsystems.Wire() returns it in mount order; main threads that
	// slice into cloud.Serve — the composition root, no init()-registry.
	"github.com/hanzoai/cloud/subsystems"
)

func main() {
	ctx := context.Background()
	shutdown := initTelemetry(ctx, "hanzo-cloud")
	defer shutdown(ctx)

	// nil ⇒ honor cfg.Enable from flags/env (empty = all subsystems).
	if err := cloud.Serve(subsystems.Wire(), nil); err != nil {
		fmt.Fprintf(os.Stderr, "cloud: %v\n", err)
		os.Exit(1)
	}
}
