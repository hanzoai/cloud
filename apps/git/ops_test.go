package git

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// typedOpCount is git's typed-op surface: every /v1/git route with a real
// request/response shape. The four creators (repo, ssh key, subscription, mirror
// target) answer 201 and zip's typed registrar has no status seam, so they stay
// raw; the pack protocol, the ZAP envelope adapters and the HTML UI have no JSON
// shape at all. Change this number only by moving a route between the two.
// 20 since browseFiles — the delivery inventory read (browse.go).
const typedOpCount = 20

// TestTypedOpsProject pins the payoff of registering ops instead of handlers:
// zip's registry — the single value REST, OpenAPI, MCP and the CLI are each
// projected from — actually holds git's surface. Only git mounts here, so every
// operation in the document is one of git's.
func TestTypedOpsProject(t *testing.T) {
	app := mountApp(t)
	app.Prepare() // installs the deferred projections a Listen would

	code, body := do(t, app, http.MethodGet, "/.well-known/openapi.json", "", nil)
	if code != http.StatusOK {
		t.Fatalf("openapi want 200, got %d (%s)", code, body)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatalf("openapi json: %v (%s)", err, body)
	}
	verbs := map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true}
	got := 0
	for p, item := range doc.Paths {
		if !strings.HasPrefix(p, "/v1/git/") {
			t.Errorf("non-git operation in the document: %s", p)
		}
		for m := range item {
			if verbs[m] {
				got++
			}
		}
	}
	if got != typedOpCount {
		t.Fatalf("typed ops in the OpenAPI projection: got %d, want %d — a route moved between typed and raw", got, typedOpCount)
	}

	// The prose half. Go drops comments at compile time, so a handler's doc
	// comment and its In/Out field docs reach the spec ONLY through the
	// build-time pass (//go:generate zipdoc → zipdoc_gen.go). A description here
	// proves the generated file is present and current; its absence means the
	// spec has shapes and no words, and zipdoc needs re-running.
	if !strings.Contains(string(body), "most recently updated") ||
		!strings.Contains(string(body), "the isolation key") {
		t.Fatal("the OpenAPI document carries no lifted prose — run `go generate ./clients/git`")
	}
}

// TestTypedOpRefusesAnonymousMCP pins the isolation half. Registering a typed op
// publishes it as an MCP tool at POST /mcp, which any caller can reach: the tool
// arguments are the whole input and there is no URL. git's org therefore comes
// from the request context (bridgePrincipal, scoped to /v1/git) and never from an
// In field — so an MCP call, which does not pass through that scope, reaches the
// op with no tenant and is refused. Any future change that lets an org arrive as
// a tool argument is a cross-tenant read, and this test is what catches it.
func TestTypedOpRefusesAnonymousMCP(t *testing.T) {
	app := mountApp(t)
	app.Prepare()

	code, body := do(t, app, http.MethodPost, "/mcp", "", map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": "get_v1_git_repos", "arguments": map[string]any{}},
	})
	if code != http.StatusOK {
		t.Fatalf("mcp want 200 (a refusal rides in the result), got %d (%s)", code, body)
	}
	if !strings.Contains(string(body), `"isError":true`) || !strings.Contains(string(body), "X-Org-Id required") {
		t.Fatalf("anonymous MCP call was not refused: %s", body)
	}
}
