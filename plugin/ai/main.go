package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/ai"
	"github.com/hanzoai/cloud/apps/zen"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the ai app, and the host of the one CO-RESIDENT app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `ai openapi`. Hand-owned — edit the spec below directly.
//
// ZEN IS HERE BECAUSE THIS IS THE ONLY PLACE IT CAN BE. It is Coresident: it
// answers no path of its own and works by wrapping ai's "/v1" — routing zen-SKU
// requests and Next()ing the rest. A middleware cannot be a separate process, so
// the host deliberately does not Load it (cmd/cloud's mount returns early on
// Coresident), and plugin/zen exists only to satisfy the gen-app-cmds bijection.
// Until this line, that left the Claim mounted in NO binary the fleet runs —
// which cmd/cloud already recorded as "zen's child never saw a request" — so zen
// SKUs served through ai's catch-all with zen's gate and meter never consulted.
//
// ORDER IS THE CONTRACT. zen mounts FIRST so its Claim is installed ahead of the
// greedy All("/v1/*") that ai registers: zip scopes middleware to the entries
// that follow it, so a Claim installed after that catch-all would sit behind the
// route it exists to gate. Claim falls through with c.Next() for everything that
// is not a zen SKU, which is what makes one prefix serve both.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "zen",
		Price:    cloud.Metered,
		Mount:    zen.Mount,
		Prefixes: manifest.GrantFor("zen"),
	}, {
		Name:  "ai",
		Price: cloud.Metered,
		App:   ai.Mount,
	}}, []string{"zen", "ai"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
