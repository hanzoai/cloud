package main

// Where a client signs in, stated once at the address the MCP server points to.
//
// The agent MCP server challenges a credential-less tools/call with a
// WWW-Authenticate header naming manifest.ResourceMetadataPath (fleet/mcp.go).
// This is that document — RFC 9728, the protected-resource metadata — and it says
// the two things an MCP client needs to run an OAuth flow on the caller's behalf:
// which resource it is about, and which authorization server mints a bearer for
// it. The authorization server is the deployment's IAM issuer, the same value the
// children validate tokens against, so a white-label deployment sends its clients
// to its own identity host.
//
// The host's, because the MCP server is the host's and because it must answer
// while every plugin is still cold. Both the root address and the path-suffixed
// one the MCP authorization spec tries first are served, by one handler.

import (
	"github.com/hanzoai/cloud/manifest"
	"github.com/zap-proto/zip"
)

// protectedResource serves the MCP server's RFC 9728 metadata on app, naming
// issuer as the authorization server. The resource is the request's own origin:
// the metadata describes whatever host the client reached, never a host it did
// not.
func protectedResource(app *zip.App, issuer string) {
	answer := func(c *zip.Ctx) error {
		origin := c.Fiber().BaseURL()
		return c.JSON(200, map[string]any{
			"resource":                 origin,
			"authorization_servers":    []string{issuer},
			"bearer_methods_supported": []string{"header"},
			"scopes_supported":         []string{"openid", "profile", "email", "offline_access"},
		})
	}
	app.Get(manifest.ResourceMetadataPath, answer)
	app.Get(manifest.ResourceMetadataPath+manifest.MCPPath, answer)
}
