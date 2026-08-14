package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/agents"
)

// agentsMount is the subsystem's own Mount, named here so coding.go can compose
// it. The indirection exists because this process serves TWO things — the agents
// subsystem and the coding engine — and only a composition root may hold both
// (apps/coding imports apps/agents, so the reverse can never be an import).
var agentsMount = agents.Mount

// Standalone entry for the agents app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `agents openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "agents",
		Price:    cloud.Metered,
		Mount:    mountAgents,
		Shutdown: shutdownAgents,
	}}, []string{"agents"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
