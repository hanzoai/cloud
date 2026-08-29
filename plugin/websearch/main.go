package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/websearch"
)

// Standalone entry for the websearch app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `websearch openapi`. Hand-owned — edit the spec below directly.
//
// Metered, not Free: two of the engines behind this surface are bought — Brave
// sells a subscription and Mojeek sells an API against a prepaid balance — so
// money moves inside the handlers even though the edge charges nothing, which is
// the one thing Metered says and Free cannot. A search is a READ, and price.go's
// Consumes makes a read free at the edge by construction, so the charge could
// never have been a number here: "such a surface meters its own units downstream
// and declares Metered." The meter is apps/websearch/meter.go, and it fires only
// for an engine whose key this deployment holds — the keyless engines stay free.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "websearch",
		Price: cloud.Metered,
		Use:   websearch.Use,
	}}, []string{"websearch"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
