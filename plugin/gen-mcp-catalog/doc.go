// gen-mcp-catalog projects client/catalog.json onto the surface an MCP client
// needs to offer the surface without asking for it, and writes that projection to
// every runtime that carries one.
//
// The grouped projection is one tool per subsystem carrying its operation names
// in an enum, and a caller fetches one operation's own prose through `describe`
// when it picks one. So a client needs the names and the count, not the prose —
// which is the difference between 640K and something a package can carry.
//
// The client cannot ask the endpoint for this: a tool list is assembled
// synchronously at registration, before any request has been made, and a client
// that needs the network to say what it can do has nothing to say when the
// network is what failed.
//
// It writes the sibling copies because the alternative is one generator per
// language, each with its own idea of the rule — which is what it replaced. This
// is NOT cloud pushing a release: the repos still build, test and publish on
// their own cadence. It writes a file into a checkout already open beside this
// one, and skips a sibling that is not there rather than inventing it.
package main
