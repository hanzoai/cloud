package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/s3"
)

// Standalone entry for the s3 app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `s3 openapi`. Hand-owned — edit the spec below directly.
//
// It also carries the object store itself — see store.go. The store starts
// FIRST because the routes are worth serving only once there is something
// behind them, and its failure is this process's failure rather than a warning:
// a plugin that answered while holding no store is the state that made a
// deleted object store invisible.
func main() {
	// Describing is not serving. `<binary> describe <dir>` projects the route set
	// from the code alone — cloud.Listen answers it before it reads config or opens
	// anything — so starting the store here would make the document a function of
	// whether the machine happened to hold an admin credential. It does not: the
	// gate ran without one and this app was the only one that could not describe
	// itself, which stopped every release.
	if _, describing := cloud.DescribeRequested(); !describing {
		if err := startStore(cloud.DataDir()); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if err := cloud.Listen([]cloud.Plugin{{
		Name:       "s3",
		Price:      cloud.Metered,
		Use:        s3.Use,
		OwnsHealth: true,
	}}, []string{"s3"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
