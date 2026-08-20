package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/compliance"
)

// Standalone entry for the compliance app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `compliance openapi`. Hand-owned — edit the spec below directly.
// Metered, not Free: starting a verification opens an inquiry at Persona, Onfido
// or Stripe — dollars per inquiry — on the DEPLOYMENT's provider key, so Hanzo is
// invoiced for whoever calls. The route is org-scoped with no admin gate, which is
// right (proving who your customer is, is the product); the gate belongs on the
// money. The meter is apps/compliance/meter.go. Everything else here reads the
// org's own rows and stays free.

func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:       "compliance",
		Price:      cloud.Metered,
		Mount:      compliance.Mount,
		Shutdown:   cloud.CtxShutdown(compliance.Shutdown),
		OwnsHealth: true,
	}}, []string{"compliance"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
