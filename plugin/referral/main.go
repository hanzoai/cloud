package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/referral"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the referrals app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `referrals openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "referral",
		Price:    cloud.Free,
		Use:      referral.Use,
		Shutdown: cloud.CtxShutdown(referral.Shutdown),
		// The /v1/<Name> default covers /v1/referral but NOT the two admin leaves
		// this subsystem also serves, /v1/admin/referral/{bonuses,sweep}: those
		// were attributed to no subsystem by cloud.Declare, and middleware the
		// subsystem installed for them through the scoped Router was recorded as an
		// escape rather than installed. The list comes from the manifest so it
		// cannot drift from the prefixes the host routes here.
		Prefixes: manifest.PrefixesFor("referral"),
	}}, []string{"referral"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
