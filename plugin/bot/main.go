package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/bot"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the bot app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `bot openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name: "bot",
		// Declared: undeclared falls back to /v1/bot, which this app does not
		// serve — the scope then guards a path with no routes and zip refuses to
		// compose (see plugin/account/main.go).
		Prefixes: manifest.PrefixesFor("bot"),
		Price:    cloud.Free,
		Mount:    bot.Mount,
		Shutdown: bot.Shutdown,
		// NO OwnsHealth. It said true and bot.Mount registers no health route at
		// all, so the field's only effect — suppressing serve.go's generic
		// GET /v1/bot/health — left this binary answering 404 there, which from
		// outside is indistinguishable from a subsystem that was never enabled.
		//
		// Nothing changes in the FLEET either way: the host routes /v1/bot/health
		// to `runtime` (whose row is the parent /v1/bot, and which forwards it to
		// the runtime's own /health), while this app's row is the three leaves
		// below it. The generic route is this binary's own STANDALONE liveness
		// answer, and that is the contract HIP-0106 states. Claim the field again
		// only alongside a real probe.
	}}, []string{"bot"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
