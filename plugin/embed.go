// Package plugin carries the fleet's build-time MCP catalogues into the host.
//
// A plugin's tool list is a function of its typed-op registry, so it is known
// when the plugin is BUILT: `<app> describe plugin/<app>` writes mcp.json beside
// openapi.json from one mount of one router (describe.go). This package embeds
// those files and hands each one to zip as Plugin.Tools, which is what lets the
// host answer tools/list for all 112 subsystems without starting a single one —
// the invariant the whole lazy fleet rests on, since MCPTools() is in-process and
// a host cannot ask a plugin that is not running.
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
