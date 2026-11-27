package git

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// typedOpCount is git's typed-op surface: every /v1/git route with a real
// request/response shape. What is left raw has no JSON shape to type: the pack
// protocol streams binary, the ZAP adapters answer an envelope a typed op cannot
// produce, the webhook's HMAC covers the raw bytes it would have to re-parse, and
// the UI serves HTML. Change this number only by moving a route between the two.
// 24 since the four creators (repo, ssh key, subscription, mirror target) went
// typed — they answer 201, which zip.WithStatus has declared since v1.18.2.
// 28 since the pull request became a noun here (pulls.go): open, list, get,
// merge. It is the last thing an agent could not do on the native plane — a run
// could push its branch and had no way to propose it — so the four are the
// surface that used to require mirroring the repository to GitHub to have one.
// 40 since git took the workflow: seven under /v1/git (declare a pool, list
// pools, list runners, list workflows, start a run, list runs, read one) and the
// five the runner daemon calls at git's OTHER root, /v1/runner. Both roots are
// git's — the manifest row says so — which is why the loop below accepts them:
// the check asks whether a FOREIGN app leaked into this document, and an app
// that answers two addresses answers two. 41 with POST /v1/git/workspace, which
// sets the org's code workspace on the forge up for its caller. 42 since a
// repository is linked rather than mirrored: POST /repos/:name/link replaced
// POST /repos/:name/mirror, and POST /v1/git/link links an org's rows at once.
// 43 with POST /repos/:name/home, which moves a repository's history to the forge.
// 40 since the SSH listener and its key registry (/v1/git/keys, three ops) went:
// the forge serves SSH for every repository whose home it is. 41 with GET
// /v1/git/activity, the estate's year of work. 43 with GET /v1/git, the
// repositories on the forge, and GET /v1/git/runs/:id/logs, what a run's jobs
// wrote — /v1/git being the product's own address, which the loop below accepts
// as git's. 50 with GET and POST /v1/admin/git/metrics, the estate's open source
// figures (#456), in the operator's family /v1/admin/git.
const typedOpCount = 45

// TestTypedOpsProject pins the payoff of registering ops instead of handlers:
// zip's registry — the single value REST, OpenAPI, MCP and the CLI are each
// projected from — actually holds git's surface. Only git mounts here, so every
// operation in the document is one of git's.
func TestTypedOpsProject(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)

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
			if p != "/v1/git" && !strings.HasPrefix(p, "/v1/git/") && !strings.HasPrefix(p, RunnerRoute+"/") && !strings.HasPrefix(p, "/v1/admin/git/") {
				t.Errorf("non-git operation in the document: %s", p)
			}
			for m := range item {
				if verbs[m] {
					got++
				}
			}
		}
		wantOps := typedOpCount
		if RunnerMount != nil {
			wantOps += 5
		}
		if got != wantOps {
			t.Fatalf("typed ops in the OpenAPI projection: got %d, want %d — a route moved between typed and raw", got, wantOps)
		}

		// The prose half. Go drops comments at compile time, so a handler's doc
		// comment and its In/Out field docs reach the spec ONLY through the
		// build-time pass (//go:generate zipdoc → zipdoc_gen.go). A description here
		// proves the generated file is present and current; its absence means the
		// spec has shapes and no words, and zipdoc needs re-running.
		if !strings.Contains(string(body), "most recently updated") ||
			!strings.Contains(string(body), "the isolation key") {
			t.Fatal("the OpenAPI document carries no lifted prose — run `go generate -run zipdoc ./apps/git`")
		}
	})
}

// TestTypedOpRefusesAnonymousMCP pins the isolation half. Registering a typed op
// publishes it as an MCP tool at POST /mcp, which any caller can reach: the tool
// arguments are the whole input and there is no URL. git's org therefore comes
// from the request context (bridgePrincipal, scoped to /v1/git) and never from an
// In field — so an MCP call, which does not pass through that scope, reaches the
// op with no tenant and is refused. Any future change that lets an org arrive as
// a tool argument is a cross-tenant read, and this test is what catches it.
func TestTypedOpRefusesAnonymousMCP(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)

		code, body := do(t, app, http.MethodPost, "/mcp", "", map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "get_git_repos", "arguments": map[string]any{}},
		})
		if code != http.StatusOK {
			t.Fatalf("mcp want 200 (a refusal rides in the result), got %d (%s)", code, body)
		}
		if !strings.Contains(string(body), `"isError":true`) || !strings.Contains(string(body), "a validated principal is required") {
			t.Fatalf("anonymous MCP call was not refused: %s", body)
		}
	})
}

// TestTypedOpServesValidatedMCP is the other half of the rule above, and the one
// that was missing. The refusal test pinned that an ANONYMOUS tool call resolves
// no tenant; nothing pinned that a call carrying one resolves it — and it did
// not. bridgePrincipal runs on /v1/git and a tools/call has no route, so every
// git tool answered "an org scope is required" to a fully validated caller, which
// is every tool call an agent run makes. tenantOf reads the caller zip carries
// when the bridge parked nothing, so the two paths resolve the same tenant.
func TestTypedOpServesValidatedMCP(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)

		if code, body := do(t, app, http.MethodPost, "/v1/git/repos", "acme",
			map[string]any{"name": "widgets"}); code != http.StatusCreated {
			t.Fatalf("create want 201, got %d (%s)", code, body)
		}
		// Another tenant's repo, so a leak is visible rather than inferred.
		if code, body := do(t, app, http.MethodPost, "/v1/git/repos", "globex",
			map[string]any{"name": "sprockets"}); code != http.StatusCreated {
			t.Fatalf("create want 201, got %d (%s)", code, body)
		}

		code, body := do(t, app, http.MethodPost, "/mcp", "acme", map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": "get_git_repos", "arguments": map[string]any{}},
		})
		if code != http.StatusOK {
			t.Fatalf("mcp want 200, got %d (%s)", code, body)
		}
		if strings.Contains(string(body), `"isError":true`) {
			t.Fatalf("a validated MCP call was refused: %s", body)
		}
		if !strings.Contains(string(body), "widgets") {
			t.Fatalf("the caller's own repo is missing: %s", body)
		}
		if strings.Contains(string(body), "sprockets") {
			t.Fatalf("another tenant's repo leaked: %s", body)
		}
	})
}

// TestTypedOpRefusesOrgWithoutUserMCP is the tenancy floor under that: an org
// header alone is not a principal. The identity boundary restores a client's
// X-Org-Id on the bearer-less path and never restores X-User-Id, so this IS the
// shape a forged public call arrives in — and it must resolve no tenant here for
// the same reason it resolves none on the REST path.
func TestTypedOpRefusesOrgWithoutUserMCP(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)

		req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,`+
			`"method":"tools/call","params":{"name":"get_git_repos","arguments":{}}}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Org-Id", "acme") // and NO X-User-Id
		resp, err := app.Test(req, testCfg)
		if err != nil {
			t.Fatalf("Test: %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if !strings.Contains(string(body), `"isError":true`) ||
			!strings.Contains(string(body), "a validated principal is required") {
			t.Fatalf("an org with no validated user was not refused: %s", body)
		}
	})
}
