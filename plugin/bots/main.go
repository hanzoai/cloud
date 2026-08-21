package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/bots"
)

// Standalone entry for the bot app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `bot openapi`. Hand-owned — edit the spec below directly.
//
// IT DECLARES NO Prefixes any more. The app used to serve three leaves —
// /v1/bots/{connect,nodes,peer/invoke} — while a SECOND app answered the parent,
// so the /v1/<name> default would have guarded a path this binary did not own and
// the routing table had to be restated here. One app owns /v1/bots now, so the
// convention names exactly what it serves.
//
// NO OwnsHealth. It said true while Mount registered no health route at all, so
// the field's only effect — suppressing serve.go's generic GET /v1/bots/health —
// left this binary answering 404 there, which from outside is indistinguishable
// from a subsystem that was never enabled. The generic route is this binary's own
// STANDALONE liveness answer, and that is the contract HIP-0106 states; the
// runtime's own probe is a relay at /v1/bots/runtime/health. Claim the field again
// only alongside a real probe.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "bots",
		Price:    cloud.Free,
		Mount:    bots.Mount,
		Shutdown: bots.Shutdown,
	}}, []string{"bots"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
