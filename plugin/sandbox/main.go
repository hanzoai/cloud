package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/sandbox"
)

// Standalone entry for the sandbox app.
//
// Without this file apps/sandbox compiles and serves nothing: the host loads a
// sibling binary per manifest entry, so an app with a Mount and no plugin/<app>
// is a package nothing links. That was the state for as long as the package
// existed — `curl api.hanzo.ai/v1/sandbox/boxes` answered 404 while the code to
// answer it sat in the tree — and it is why every consumer that points at the
// executor (hanzo.app's ProjectFs, apps/exec's upstream, apps/functions'
// invoke) had nothing behind it.
//
// It is the SCHEDULER, not the executor. It claims a box, tracks its lease, and
// forwards fs/proc/git through to the boxd running inside it; the work happens
// in the pod. Free at this tier because the box itself is what gets metered —
// billing for the call that starts a pod and again for the pod would charge
// twice for one thing.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "sandbox",
		Price: cloud.Free,
		Mount: sandbox.Mount,
	}}, []string{"sandbox"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
