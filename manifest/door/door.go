// Package door is the fleet's agent MCP server as three addresses, and nothing
// else.
//
// It is a LEAF on purpose: the routing table (manifest) states the MCP
// server's address and the document (openapi) projects it, and neither may
// import the other — each has tests that reach into the other's package, which
// Go refuses as a cycle. A value both need lives where both can read it
// without reading each other, the way brand does for the issuer.
package door

const (
	// Path is the fleet's one agent MCP address: POST /v1/mcp, served by the host
	// that fronts the API. See manifest.MCPPath for why it is this address.
	Path = "/v1/mcp"

	// Framework is zip's built-in default — where a plugin serves its OWN MCP
	// server, and the address the host signposts to Path. See
	// manifest.FrameworkMCPPath.
	Framework = "/mcp"

	// Metadata is where the host publishes RFC 9728 metadata for the MCP server:
	// which authorization server mints the bearer a tools/call needs. The MCP
	// server's WWW-Authenticate challenge names it (fleet/mcp.go) and the host
	// serves it (cmd/cloud/oauth.go) — one name, two readers, so the challenge
	// can never point at an address the host does not answer.
	Metadata = "/.well-known/oauth-protected-resource"
)
