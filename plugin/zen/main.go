package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/zen"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the zen app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `zen openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "zen",
		Price: cloud.Metered,
		Mount: zen.Mount,
		// WHAT ZEN GATES, WHICH IS NOT WHAT ZEN ROUTES. These are two facts and
		// this repo has one field for each: manifest.App.Prefixes is "the absolute
		// paths it answers" (the host's routing table) and cloud.Plugin.Prefixes is
		// "the route subtrees whose middleware this subsystem may install".
		//
		// zen answers NO path — it is Coresident, a Claim middleware on ai's
		// router — so its manifest row correctly states no prefix. This read that
		// row (manifest.PrefixesFor("zen") == nil) and fed a ROUTING answer to a
		// MIDDLEWARE question, so the scope zen got owned only the conventional
		// "/v1/zen"; installing the Claim on "/v1" then escaped it and MountAll
		// refused the mount outright. Braiding the two is what broke it: dropping
		// "/v1" from the manifest row was right for routing (it duplicated ai's
		// claim) and silently revoked the gate.
		//
		// So the manifest carries BOTH facts — Prefixes for routing, Gates for the
		// grant — and this reads the one it needs. Still no literal here: a
		// restated prefix is the drift TestNoPluginRestatesItsPrefixes exists to
		// make unrepresentable, and it costs an outage when it happens.
		Prefixes: manifest.GrantFor("zen"),
	}}, []string{"zen"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
