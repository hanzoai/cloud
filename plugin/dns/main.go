package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/dns"
)

// Standalone entry for the dns app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `dns openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "dns",
		Price: cloud.Free,
		Use:   dns.Use,
		// The plane's own /v1/dns/health is one of the addresses Mount declares,
		// so serve.go must not declare it too: one address claimed twice is a
		// composition zip refuses outright, and the subsystem would not start.
		OwnsHealth: true,
	}}, []string{"dns"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
