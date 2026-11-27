package main

import (
	"context"
	"fmt"
	"os"

	"github.com/hanzoai/cloud/apps/iam"
	_ "github.com/hanzoai/cloud/internal/driver"
	_ "github.com/hanzoai/cloud/internal/host"
	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk"
)

// Standalone entry for the iam app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `iam openapi`. Hand-owned — edit the spec below directly.
func main() {
	// `iam copy` moves the identity store between this pod's file and hanzoai/sql
	// and proves the two equal (apps/iam/copy.go). It serves nothing.
	if len(os.Args) > 1 && os.Args[1] == "copy" {
		if err := iam.RunCopy(context.Background(), os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "iam:", err)
			os.Exit(1)
		}
		return
	}
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "iam",
		Price:    cloud.Free,
		Use:      iam.Use,
		Shutdown: cloud.CtxShutdown(iam.Shutdown),
		Prefixes: manifest.PrefixesFor("iam"),
	}}, []string{"iam"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
