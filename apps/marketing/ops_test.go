package marketing

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// typedOpCount is marketing's whole /v1/marketing surface. Every route is a
// typed op — there is no raw handler left in this package — so this number is
// also the route count. Change it only by adding or removing a route.
const typedOpCount = 35

// raw issues one request and returns the untouched body, for the projections
// (OpenAPI, MCP) that are not org-scoped JSON records.
func raw(t *testing.T, app *zip.App, method, path, body string) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

// TestTypedOpsProject pins the payoff of registering ops instead of handlers:
// zip's registry — the single value REST, OpenAPI, MCP and the CLI are each
// projected from — actually holds marketing's whole surface. Only marketing
// mounts here, so every operation in the document is one of marketing's.
func TestTypedOpsProject(t *testing.T) {
	app, _ := mountRoutes(t)

	code, body := raw(t, app, http.MethodGet, "/.well-known/openapi.json", "")
	if code != http.StatusOK {
		t.Fatalf("openapi want 200, got %d (%s)", code, body)
	}
	var doc struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("openapi json: %v", err)
	}
	verbs := map[string]bool{"get": true, "post": true, "put": true, "patch": true, "delete": true}
	got := 0
	for p, item := range doc.Paths {
		if !strings.HasPrefix(p, "/v1/marketing/") {
			t.Errorf("non-marketing operation in the document: %s", p)
		}
		for m := range item {
			if verbs[m] {
				got++
			}
		}
	}
	if got != typedOpCount {
		t.Fatalf("typed ops in the OpenAPI projection: got %d, want %d — a route was added, removed, or left raw", got, typedOpCount)
	}

	// The prose half. Go drops comments at compile time, so a handler's doc
	// comment and its In/Out field docs reach the spec ONLY through the
	// build-time pass (//go:generate zipdoc → zipdoc_gen.go). Finding the words
	// here proves the generated file is present and current; their absence means
	// the spec has shapes and no words.
	for _, want := range []string{
		"fans the sequence out over a saved audience", // an In field's doc
		// A handler's doc, WITHOUT the leading `previewAudience` its source comment
		// opens with: the identifier belongs to Go's namespace, and zip strips an
		// exact leading match of the handler's own name on the way out (v1.18.13).
		"Evaluates the cohort LIVE",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("the OpenAPI document is missing lifted prose %q — run `go generate ./clients/marketing`", want)
		}
	}
}

// TestTypedOpRefusesAnonymousMCP pins the isolation half. Registering a typed op
// publishes it as an MCP tool at POST /mcp, where the tool arguments are the
// whole input and any caller can reach it. marketing's org therefore comes from
// the request context (cloud.Bridge, scoped to /v1/marketing) and never from an
// In field — so an anonymous call, which passes through no such scope and
// carries no validated principal, reaches the op with no tenant and is refused.
// Any future change that lets an org arrive as a tool argument is a cross-tenant
// read, and this test is what catches it.
func TestTypedOpRefusesAnonymousMCP(t *testing.T) {
	app, _ := mountRoutes(t)

	code, body := raw(t, app, http.MethodPost, "/mcp",
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"get_marketing_campaigns","arguments":{}}}`)
	if code != http.StatusOK {
		t.Fatalf("mcp want 200 (a refusal rides in the result), got %d (%s)", code, body)
	}
	if !strings.Contains(body, `"isError":true`) || !strings.Contains(body, "X-Org-Id required") {
		t.Fatalf("anonymous MCP call was not refused: %s", body)
	}
}

// TestTypedOpStatusCodes pins the two statuses a typed op does not write by
// default. zip answers 200, or 204 for a nil Out; a creator says 201 through
// cloud.Created, which the bridge applies after the handler returns. Converting
// these routes without that seam would silently downgrade every create to 200,
// which is a wire break for any client that checks — so this asserts the wire,
// not the handler.
func TestTypedOpStatusCodes(t *testing.T) {
	app, _ := mountRoutes(t)

	code, camp := call(t, app, http.MethodPost, "/v1/marketing/campaigns", "hanzo",
		`{"name":"Spring Launch","channel":"meta","budget":50000}`)
	if code != http.StatusCreated {
		t.Fatalf("create campaign want 201, got %d (%v)", code, camp)
	}
	id := str(camp, "id")

	if code, _ := call(t, app, http.MethodGet, "/v1/marketing/campaigns/"+id, "hanzo", ""); code != http.StatusOK {
		t.Fatalf("get campaign want 200, got %d", code)
	}
	if code, _ := call(t, app, http.MethodDelete, "/v1/marketing/campaigns/"+id, "hanzo", ""); code != http.StatusNoContent {
		t.Fatalf("delete campaign want 204, got %d", code)
	}
	// A second delete finds nothing — the 404 still comes from the error path,
	// not from the status seam.
	if code, _ := call(t, app, http.MethodDelete, "/v1/marketing/campaigns/"+id, "hanzo", ""); code != http.StatusNotFound {
		t.Fatalf("delete twice want 404, got %d", code)
	}
}

// TestTypedOpBindsURL pins that a typed op sees its own URL: the path id and the
// query filter both bind onto the In, which is what let these routes become ops
// at all. A regression here would silently widen a filtered list to everything.
func TestTypedOpBindsURL(t *testing.T) {
	app, _ := mountRoutes(t)

	for _, status := range []string{"active", "draft"} {
		if code, out := call(t, app, http.MethodPost, "/v1/marketing/campaigns", "hanzo",
			`{"name":"c-`+status+`","status":"`+status+`"}`); code != http.StatusCreated {
			t.Fatalf("seed %s: %d %v", status, code, out)
		}
	}
	// ?status= narrows (a query param reaching the In).
	_, list := call(t, app, http.MethodGet, "/v1/marketing/campaigns?status=active&limit=50", "hanzo", "")
	rows, _ := list["data"].([]any)
	if len(rows) != 1 {
		t.Fatalf("?status=active want 1 campaign, got %d (%v)", len(rows), list)
	}
	// An unparseable limit falls back to the default rather than refusing the
	// call, exactly as the untyped handler's strconv.Atoi did.
	if code, _ := call(t, app, http.MethodGet, "/v1/marketing/campaigns?limit=abc", "hanzo", ""); code != http.StatusOK {
		t.Fatalf("?limit=abc want 200, got %d", code)
	}
	// The path id reaching the In: an id no org owns is a 404, not an empty read.
	if code, _ := call(t, app, http.MethodGet, "/v1/marketing/campaigns/camp_nope", "hanzo", ""); code != http.StatusNotFound {
		t.Fatalf("unknown id want 404, got %d", code)
	}
}

// TestTypedOpRefusesUnvalidatedPrincipal is the forge case: an off-gateway caller
// presenting only X-Org-Id, with no validated user. principal.Org refuses it, so
// the bridge parks nothing and every org-scoped op answers 403 — the same answer
// the raw handlers gave.
func TestTypedOpRefusesUnvalidatedPrincipal(t *testing.T) {
	app, _ := mountRoutes(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/marketing/campaigns", nil)
	req.Header.Set("X-Org-Id", "victim") // no X-User-Id: nothing validated this
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unvalidated principal want 403, got %d", resp.StatusCode)
	}
}
