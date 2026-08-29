package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/sandbox"
)

// Standalone entry for the sandboxes app.
//
// This is the app's OWN composition root — it links only apps/sandbox and the
// cloud request tier, never package apps, so the build is this one subsystem
// and not the whole fleet. The light host loads it as a plugin; run directly it
// serves standalone. Its OpenAPI subset comes from `sandboxes openapi`.
//
// Scaffolded by plugin/gen-app-cmds from the manifest.Apps row; now hand-owned.
//
// Metered, not Free, and that is a statement about WHERE the charge lives rather
// than about its size. A lease is gated and debited by apps/sandbox itself
// (Lease, around the pod), so a meter downstream of the edge owns it — which is
// exactly what Metered means and what Free denied. What it currently charges is
// zero, deliberately: every class has been free since sandboxes existed,
// ResourceFee says why, and a test pins it. Declaring the surface honestly and
// pricing it are two different changes, and this is only the first.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "sandbox",
		Price: cloud.Metered,
		Use:   sandbox.Use,
		// The org stores drain on the way out: a lease ended just before SIGTERM
		// has to reach the object store, or the successor hydrates it as still
		// running and bills the customer for a sandbox that is gone.
		Shutdown: cloud.CtxShutdown(sandbox.Shutdown),
	}}, []string{"sandbox"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
