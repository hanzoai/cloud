package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/exec"
)

// Standalone entry for the exec app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone.
//
// Its OpenAPI subset is GENERATED, never written: `make -C apps/exec openapi`
// runs this binary's own `exec openapi` over its own live router and writes
// plugin/exec/openapi.json (mk/plugin.mk). Hand-editing that file is how a spec
// starts disagreeing with the router; on a merge conflict, rebase and regenerate.
// This file is the hand-owned half — a Shutdown/OwnsHealth goes here if the app
// grows to need one.
//
// Metered, not Free: running a snippet leases a sandbox and executes a program in
// it — a pod and a slice of a node for as long as the program takes. The meter is
// apps/exec/meter.go, and it sits on this subsystem's own endpoint rather than inside
// the exported interpreter, because apps/functions composes that same call and
// already charges its own invoke fee for it.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "exec",
		Price: cloud.Metered,
		Use:   exec.Use,
	}}, []string{"exec"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
