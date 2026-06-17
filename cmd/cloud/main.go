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

	// Every subsystem registers into cloud.Registry via init(); the set is
	// defined ONCE in the subsystems bundle (one source of truth, shared with
	// cmd/hanzo). Blank-importing it populates the registry cloud.Serve mounts.
	_ "github.com/hanzoai/cloud/subsystems"
)

func main() {
	// nil ⇒ honor cfg.Enable from flags/env (empty = all subsystems).
	if err := cloud.Serve(nil); err != nil {
		fmt.Fprintf(os.Stderr, "cloud: %v\n", err)
		os.Exit(1)
	}
}
