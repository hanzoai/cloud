package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/company"
)

// Standalone entry for the company app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `company openapi`. Hand-owned — edit the spec below directly.
// Metered, not Free — and the meter was already there. apps/company charges the
// $999 formation (providers.go, gate + debit, kind "company-formation"); only the
// declaration had not caught up, so the edge required no standing for a surface
// that moves a four-figure sum. The on-chain genesis anchor is one act INSIDE that
// paid formation, gated by the same stage machine, so it is covered rather than
// separately priced.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "company",
		Price:    cloud.Metered,
		Use:      company.Use,
		Shutdown: company.Shutdown,
	}}, []string{"company"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
