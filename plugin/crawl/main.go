package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/crawl"
)

// Standalone entry for the crawl app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `crawl openapi`. Hand-owned — edit the spec below directly.
//
// Metered, not Free: a page that renders leaves this process for a headless
// browser that holds a pod for up to forty-five seconds, which is dedicated
// compute of the same class the fleet already meters for studio renders and ML
// predicts. Reading a page is a READ, so price.go's Consumes makes it free at
// the edge by construction and the charge could never have been a number here.
// The meter is apps/crawl/meter.go, and it fires only on the escalation — the
// static fetch beside it is one http.Get and stays free.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "crawl",
		Price: cloud.Metered,
		Mount: crawl.Mount,
	}}, []string{"crawl"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
