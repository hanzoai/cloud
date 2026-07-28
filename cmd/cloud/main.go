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
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/writerpin"
	"github.com/hanzoai/cloud/writerpin/lease"

	// The subsystem set is defined ONCE in the subsystems bundle (shared with
	// cmd/hanzo). apps.Wire() returns it in mount order; main threads that
	// slice into cloud.Serve — the composition root, no init()-registry. Linking
	// subsystems also links clients/o11y, whose init() registers the telemetry
	// bootstrap cloud.Serve runs (cloud.RegisterTelemetryInstaller).
	"github.com/hanzoai/cloud/apps"
)

func main() {
	// Register the cluster elector HERE, at the composition root, because this is
	// the binary that runs in a cluster. writerpin declares the Pin interface and
	// the two implementations that need no cluster; writerpin/lease is the only
	// code that imports client-go. Keeping that import on this side of the seam is
	// what keeps 400 k8s.io packages out of github.com/hanzoai/cloud, and therefore
	// out of every plugin binary that only wanted cloud.Deps.
	writerpin.UseElector(lease.Elect)

	// Telemetry is bootstrapped inside cloud.Serve (one site, every entrypoint —
	// cmd/cloud AND every `hanzo <svc>`), so main() is just the full-surface
	// entrypoint. nil ⇒ honor cfg.Enable from flags/env (empty = all subsystems).
	if err := cloud.Serve(apps.Wire(), nil); err != nil {
		fmt.Fprintf(os.Stderr, "cloud: %v\n", err)
		os.Exit(1)
	}
}
