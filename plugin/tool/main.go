package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the tools app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `tools openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "tool",
		Price: cloud.Metered,
		// The registry views (/v1/skills, /v1/mcp/servers, /v1/plugins) are this
		// subsystem's surface too, so they must be declared or a request to one
		// resolves to no subsystem and its price is Undeclared. Kept in sync
		// with manifest/apps.go, which states the same thing for the fused host.
		Prefixes: manifest.PrefixesFor("tool"),
		Use:      tools.Use,
		Shutdown: tools.Shutdown,
		// The per-caller half of the fleet's ONE MCP server. This app's typed ops are
		// projected into mcp.json at build time like every other app's; what cannot
		// be projected is the caller's OWN tools — its connectors, skills, agents,
		// and the external servers it enabled — because those are rows. The host
		// declares this app Open (manifest/apps.go) and asks it per caller.
		Offer: tools.Offer(),
	}}, []string{"tool"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
