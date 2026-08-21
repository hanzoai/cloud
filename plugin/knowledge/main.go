package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/knowledge"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the knowledge app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `knowledge openapi`. Hand-owned — edit the spec below directly.
//
// Metered, not Free: a long-tail connector's sync executes a JavaScript piece on
// the auto engine's sandbox pods — the same capacity plugin/auto owns and prices.
// Reaching it by in-cluster URL instead of through its door does not make the pod
// cheaper. The meter is apps/knowledge/meter.go, and it fires only on a PIECE
// run: the native-Go connectors beside it start no pod and stay free.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name: "knowledge",
		// Declared: undeclared falls back to /v1/knowledge, which this app does not
		// serve — the scope then guards a path with no routes and zip refuses to
		// compose (see plugin/account/main.go).
		Prefixes: manifest.PrefixesFor("knowledge"),
		Price:    cloud.Metered,
		Mount:    knowledge.Mount,
	}}, []string{"knowledge"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
