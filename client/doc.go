//go:generate go run ./gen

// Package client is the host's client for every app it runs: one process per
// API beside it, and this is how the host reaches them.
//
// It answers a question about a subsystem by ASKING the subsystem, over that
// app's own unix socket, and composes the answers into one surface.
//
// The host links no subsystem (cmd/cloud), so it cannot read a registry it does
// not hold. For a long time it read a COMMITTED PROJECTION instead —
// plugin/<app>/mcp.json, the tool array each app's binary wrote when it was
// built, embedded into the host and handed to zip as Plugin.Tools. That file was
// a second source for a fact the child already knows, and a second source can
// only be stale or accidentally correct. It was stale: plugin/o11y/mcp.json held
// 12 tools while the o11y binary at the same commit served 365, because the 353
// missing ops live in github.com/hanzoai/o11y and a dependency bump in another
// repository invalidated an artifact in this one with nothing in the diff to say
// so.
//
// Regenerating that file more often does not fix it. A generator on a hook is
// still two sources with a race between them, and the trigger here is in a
// different repository, so no hook in this one can see it. The fix is that the
// file stops existing and the host asks.
//
// # What asking costs, and why it is affordable
//
// A child answers on its OWN MCP server — zip's default /mcp, which cloud.Serve
// deliberately leaves where the framework puts it (manifest.FrameworkMCPPath),
// over the private ZAP socket the host started it on; or, for a caller that
// reached this host from INSIDE, the same MCP server on the app's own plane
// socket (manifest.MCPPath, cloud.UseMCP). [At] resolves the name to whichever of
// the two that caller may use, and zip.App.Start is what makes either reachable:
// idempotent, and the SAME single-flighted path a request to the app's prefix
// takes, so a burst of concurrent askers still produces one child.
//
// So the first ask of a cold app pays that app's start. That is the cost of the
// answer being true, and it is paid once per app per host: the child stays up
// afterwards, and every later ask is a unix round trip. Nothing is cached
// between requests, because a cache of a catalogue IS the file this package
// exists to delete.
//
// # A subsystem that does not answer is REPORTED
//
// [Answer.Err] is never swallowed. A catalogue that silently drops the app it
// could not reach is indistinguishable from one whose app serves nothing, and
// those are the same defect the stale file was: the caller cannot tell. Every
// caller here reports its failures by name — see [MCP] for the wire shape.
package client
