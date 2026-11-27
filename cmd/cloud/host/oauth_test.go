package host

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/sdk/mcp"
	"github.com/zap-proto/zip"
)

// The MCP server's sign-in discovery names the deployment's own issuer, at both
// addresses the MCP authorization flow tries, and describes the host the client
// actually reached: RFC 9728 complete, with the scopes a client asks for, the
// API's name and where it is documented.
func TestProtectedResourceNamesTheIssuer(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	openapi.UseAgent(app, nil, nil, "https://hanzo.id", "hanzo")

	for p, resource := range map[string]string{
		manifest.ResourceMetadataPath: "http://cloud",
		mcp.MetadataMCP:               "http://cloud" + manifest.MCPPath,
	} {
		code, ctype, body := do(t, app, p)
		if code != 200 {
			t.Fatalf("GET %s = %d, want 200: %s", p, code, body)
		}
		if !strings.Contains(ctype, "application/json") {
			t.Errorf("GET %s Content-Type = %q, want JSON", p, ctype)
		}
		var meta openapi.Resource
		if err := json.Unmarshal([]byte(body), &meta); err != nil {
			t.Fatalf("GET %s: %v: %s", p, err, body)
		}
		if meta.Resource != resource {
			t.Errorf("GET %s: resource = %q, want %q: a client checks it against the URL it called", p, meta.Resource, resource)
		}
		if len(meta.Servers) != 1 || meta.Servers[0] != "https://hanzo.id" {
			t.Errorf("authorization_servers = %v, want the issuer", meta.Servers)
		}
		if len(meta.Methods) != 1 || meta.Methods[0] != "header" {
			t.Errorf("bearer_methods_supported = %v, want [header]", meta.Methods)
		}
		if strings.Join(meta.Scopes, " ") != "openid profile email offline_access" {
			t.Errorf("scopes_supported = %v", meta.Scopes)
		}
		if meta.Name != "Hanzo API" || meta.Documentation != "https://docs.hanzo.ai" {
			t.Errorf("resource_name = %q, resource_documentation = %q", meta.Name, meta.Documentation)
		}
	}
}
