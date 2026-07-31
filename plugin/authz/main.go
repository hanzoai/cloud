package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/authz/serve"
	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// Standalone entry for the authz app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `authz openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Serve([]cloud.Plugin{{
		Name:  "authz",
		Price: cloud.Free,
		// The adapter lives HERE, on cloud's side: authz is a leaf and must never
		// import cloud, so cloud's plugin contract bends to the leaf rather than the
		// leaf learning about Deps.
		App: func(app *zip.App, deps cloud.Deps) error { return serve.Mount(app, deps.Logger) },
	}}, []string{"authz"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
