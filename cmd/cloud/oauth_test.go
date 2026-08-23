package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/manifest"
	"github.com/zap-proto/zip"
)

// The MCP server's sign-in discovery names the deployment's own issuer, at both
// addresses the MCP authorization flow tries, and describes the host the client
// actually reached.
func TestProtectedResourceNamesTheIssuer(t *testing.T) {
	app := zip.New(zip.Config{DisableStartupMessage: true})
	protectedResource(app, "https://hanzo.id")

	for _, p := range []string{manifest.ResourceMetadataPath, manifest.ResourceMetadataPath + manifest.MCPPath} {
		code, ctype, body := do(t, app, p)
		if code != 200 {
			t.Fatalf("GET %s = %d, want 200: %s", p, code, body)
		}
		if !strings.Contains(ctype, "application/json") {
			t.Errorf("GET %s Content-Type = %q, want JSON", p, ctype)
		}
		var meta struct {
			Resource string   `json:"resource"`
			Servers  []string `json:"authorization_servers"`
			Methods  []string `json:"bearer_methods_supported"`
		}
		if err := json.Unmarshal([]byte(body), &meta); err != nil {
			t.Fatalf("GET %s: %v: %s", p, err, body)
		}
		if meta.Resource != "http://cloud" {
			t.Errorf("resource = %q, want the origin the client reached", meta.Resource)
		}
		if len(meta.Servers) != 1 || meta.Servers[0] != "https://hanzo.id" {
			t.Errorf("authorization_servers = %v, want the issuer", meta.Servers)
		}
		if len(meta.Methods) != 1 || meta.Methods[0] != "header" {
			t.Errorf("bearer_methods_supported = %v, want [header]", meta.Methods)
		}
	}
}
