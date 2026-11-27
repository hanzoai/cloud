package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud/apps/affiliate"
	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk"
)

// Standalone entry for the affiliates app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `affiliates openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "affiliate",
		Price:    cloud.Free,
		Use:      affiliate.Use,
		Shutdown: cloud.CtxShutdown(affiliate.Shutdown),
		Prefixes: manifest.PrefixesFor("affiliate"),
	}}, []string{"affiliate"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
