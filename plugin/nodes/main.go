package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/nodes"
)

// Standalone entry for the nodes app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `nodes describe`. Hand-owned — edit the spec below directly.
//
// Shutdown ends the presence-renew loop, which releases this replica's claims on
// the way out so peers stop forwarding into a pod that is draining. It came here
// with the node plane: the loop is the node plane's, and the run plane it used to
// share a binary with had nothing to shut down at all.
//
// NO OwnsHealth. The generic GET /v1/nodes/health that serve.go registers is this
// binary's standalone liveness answer, which is the contract HIP-0106 states;
// claiming the field without a real probe leaves a 404 there, indistinguishable
// from a subsystem that was never enabled.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "nodes",
		Price:    cloud.Free,
		Mount:    nodes.Mount,
		Shutdown: nodes.Shutdown,
	}}, []string{"nodes"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
