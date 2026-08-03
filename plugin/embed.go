// Package plugin carries the fleet's build-time PROJECTION into the host: the
// OpenAPI subset each subsystem serves, known without running it.
//
// It is a function of the plugin's own router, so it is known when the plugin is
// BUILT: `<app> describe plugin/<app>` writes openapi.json from one mount of one
// router (describe.go). This package embeds those files, and the host weaves them
// into the fleet document it serves at /v1/openapi.json.
//
// It exists as its own leaf package for one reason: go:embed cannot reach outside
// its own directory, so the bytes must be embedded from HERE, and cmd/cloud must
// stay light. This file imports embed and nothing else — no apps, no cloud root —
// so `go list -deps ./cmd/cloud` gains exactly one package and still links no
// subsystem.
//
// # There is no MCP catalogue here any more
//
// It used to embed plugin/<app>/mcp.json too — the tool array the app's binary
// projected at build time — and hand it to zip as Plugin.Tools so the host could
// answer tools/list without touching a child. That file was a SECOND source for a
// fact the child already knows, and a second source can only be stale or
// accidentally correct. It was stale: plugin/o11y/mcp.json held 12 tools while
// the o11y binary at the same commit served 365, because the missing 353 ops live
// in github.com/hanzoai/o11y and a go.mod bump in ANOTHER repository invalidated
// an artifact in this one with nothing in the diff to say so. No generator on a
// hook in this repo could have seen that trigger.
//
// So the catalogue is not regenerated more often; it is gone, and the door asks
// the child (package fleet). The subset below survives for the one reason the
// catalogue could not: the fleet's document carries each subsystem's PROSE, and
// that prose is lifted from the app's SOURCE at describe time
// (openapi.Synopsis) — a running child has no comment to read and would answer
// with its deployment's brand blurb instead, which the weave would then publish
// as the description of every product tag. Making the synopsis a declared value
// is what the other half of this file is waiting on.
package plugin

import "embed"

// specs holds every app's openapi.json — the same committed files the drift gate
// regenerates from source and the weave composes into openapi.yaml
// (mk/fleet.mk surface-check). Embedding them is what lets the host describe the
// whole fleet without starting any of it: the alternative is reading the live
// router, and the light host's live router is 113 proxy prefixes.
//
// A glob, so an app that has not been described yet is absent rather than a build
// failure in the host. Absence is then refused where it can be reported —
// openapi.Subsets, at the request that needs it — instead of by a compiler error
// nobody can act on.
//
//go:embed */openapi.json
var specs embed.FS

// Spec is app's own OpenAPI subset: the document its binary projected from its
// own router at build time. Nil for an app that ships none.
func Spec(app string) []byte {
	b, err := specs.ReadFile(app + "/openapi.json")
	if err != nil {
		return nil
	}
	return b
}
