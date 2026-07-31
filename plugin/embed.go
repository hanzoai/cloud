// Package plugin carries the fleet's build-time PROJECTIONS into the host: what
// each subsystem serves, known without running it.
//
// Both are a function of the plugin's own router and typed-op registry, so both
// are known when the plugin is BUILT: `<app> describe plugin/<app>` writes
// mcp.json and openapi.json from one mount of one router (describe.go). This
// package embeds those files. The tool lists go to zip as Plugin.Tools, which is
// what lets the host answer tools/list for all 113 subsystems without starting a
// single one; the specs are what the host weaves into the fleet document it
// serves at /v1/openapi.json. Same invariant, two consumers: MCPTools() and the
// router are in-process, and a host cannot ask a plugin that is not running.
//
// It exists as its own leaf package for one reason: go:embed cannot reach outside
// its own directory, so the bytes must be embedded from HERE, and cmd/cloud must
// stay light. This file imports embed and nothing else — no apps, no cloud root —
// so `go list -deps ./cmd/cloud` gains exactly one package and still links no
// subsystem.
//
// The catalogue can only be INCOMPLETE, never wrong: the child's own registry
// answers the call, so a name the host still lists but the child no longer serves
// yields that child's -32602 rather than a mis-dispatch.
package plugin

import "embed"

// catalogues holds every app's mcp.json. The pattern is a glob, so an app that
// has not been described yet is simply absent — Tools returns nil and zip leaves
// that plugin off the door — rather than a build failure in the host.
//
//go:embed */mcp.json
var catalogues embed.FS

// Tools is app's MCP catalogue: the JSON array its own App.MCPTools() projected
// at build time, ready to hand to zip.Plugin.Tools. Nil for an app that ships
// none, which is exactly how a plugin opts out of the composed door.
func Tools(app string) []byte {
	b, err := catalogues.ReadFile(app + "/mcp.json")
	if err != nil {
		return nil
	}
	return b
}

// specs holds every app's openapi.json — the same committed files the drift gate
// regenerates from source and the weave composes into openapi.yaml
// (mk/fleet.mk surface-check). Embedding them is what lets the host describe the
// whole fleet without starting any of it: the alternative is reading the live
// router, and the light host's live router is 113 proxy prefixes.
//
// A glob, for the same reason the catalogues are: an app that has not been
// described yet is absent rather than a build failure in the host. Absence is
// then refused where it can be reported — openapi.Subsets, at the request that
// needs it — instead of by a compiler error nobody can act on.
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
