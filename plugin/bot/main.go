package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/bot"
)

// Standalone entry for the bots app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `bots describe`. Hand-owned — edit the spec below directly.
//
// IT DECLARES NO Prefixes. One app owns /v1/bot and serves nothing outside it,
// so the /v1/<name> default names exactly what this binary answers.
//
// NO Shutdown, because there is nothing here to end. The presence-renew loop the
// field used to name belonged to the node plane, and the node plane is its own
// capability now (apps/nodes): a run is a task the executor owns and this process
// keeps no loop of its own for one.
//
// NO OwnsHealth. It said true while Mount registered no health route at all, so
// the field's only effect — suppressing serve.go's generic GET /v1/bot/health —
// left this binary answering 404 there, which from outside is indistinguishable
// from a subsystem that was never enabled. The generic route is this binary's own
// STANDALONE liveness answer, and that is the contract HIP-0106 states; the
// runtime's own probe is a relay at /v1/bot/runtime/health. Claim the field again
// only alongside a real probe.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "bot",
		Price: cloud.Free,
		Use:   bot.Use,
	}}, []string{"bot"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
