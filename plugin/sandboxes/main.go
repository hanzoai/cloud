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
// Scaffolded by plugin/gen-app-cmds from the manifest.Apps row; now hand-owned —
// add a Shutdown/OwnsHealth here if the app grows to need one.
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
		Name:  "sandboxes",
		Price: cloud.Metered,
		Mount: sandbox.Mount,
	}}, []string{"sandboxes"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
