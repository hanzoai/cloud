package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/risk"
)

// Standalone entry for the risk app — HANZO RISK's model plane.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet. The light host loads it as a
// plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `risk describe`.
//
// Price is Metered, not a flat edge price. The billable unit here is an
// EXHAUSTIVE SEARCH — real compute over a tenant's own history — and it is gated
// and metered per run inside the op; a flat per-request price at the edge would
// charge the same for reading a model's state.
//
// OwnsHealth, because the generic always-ok liveness route would shadow a real
// probe: /v1/risk/health answers 503 carrying the report when the model plane
// cannot work.
//
// Shutdown snapshots every resident model. The binary deploys one replica at a
// time with the old pod stopped first, so without it every rollout silently
// returns every tenant to warming — and a warming model refuses to score, which
// reads as a clean result to anything that does not check the refusal.
func main() {
	if err := cloud.Serve([]cloud.Plugin{{
		Name:       "risk",
		Price:      cloud.Metered,
		Mount:      risk.Mount,
		Shutdown:   risk.Shutdown,
		OwnsHealth: true,
	}}, []string{"risk"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
