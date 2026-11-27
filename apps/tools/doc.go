// Package tools is everything your org can call, in one list: connector
// actions, functions, agents, skills and your own MCP servers.
//
// It is the ONE tool plane for Hanzo Cloud's PER-TENANT capabilities — a single
// registry where every callable thing an ORG owns is a Tool with a Source, a
// JSON-Schema, a per-(org,project) activation state, and an optional price.
//
// Per-tenant is the whole boundary. Cloud's OWN typed ops are not here and never
// were a Source: they are code, known at build time, and the fleet publishes them
// as MCP tools straight from the typed-op registry onto the host's one MCP server
// (plugin/<app>/mcp.json → zip.Plugin.Tools). What lives here is ROWS — a tool
// whose existence, price and activation depend on which org is asking — reached
// from that same server through the typed POST /v1/tool/call.
//
// Decomplected on the Rich Hickey client: a Source knows how to LIST its tools and
// DISPATCH one; the registry knows nothing about how any single source works. Each
// source REGISTERS a Provider into the registry from its own Mount — no source
// duplicates listing or dispatch logic, and the registry never grows a per-source
// branch. Adding a source is: implement Provider, call tools.Register.
//
// Every dispatch flows through ONE per-principal plane: the caller's VALIDATED org
// (principal.Org) gates the call, the tool must be ACTIVATED for that (org,project)
// or the call is 403, a priced tool settles through the explicit x402 Charger client,
// and the platform meters one unit. One plane, one policy, every source.
package tools
