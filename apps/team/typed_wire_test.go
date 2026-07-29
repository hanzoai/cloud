package team

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/openapi"
)

// The routes this file pins were raw fiber handlers and are now typed ops. A
// typed op is a DESCRIPTION of a route, never a change to it, so what is tested
// here is the exact bytes: same status, same body, same headers, for every arm
// the handler has. Each assertion is the answer the untyped handler gave,
// measured before the conversion.

// TestCollabRPCShapesAreExact pins all three reply shapes of the collaborator
// RPC. They are three DIFFERENT bodies on one 200, which is why collabResult
// carries a POINTER to its content map: `{"content":{}}` (a real empty answer)
// and `{}` (updateContent, which answers nothing) must stay distinguishable, and
// a plain map with omitempty would render both as `{}`.
func TestCollabRPCShapesAreExact(t *testing.T) {
	app := mountTeam(t)
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	ws, err := mounted.State.accounts.EnsureWorkspace(t.Context(), org, acct, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	auth := bearerFor(t, acct, org)
	docID := collabDocID(ws.UUID, "tracker:class:Issue", "issue-shapes", "description")

	// getContent with NO source is a first-class case (source is optional in the
	// client contract) and it answers an empty content OBJECT, not an absent one.
	code, body := call(t, app, http.MethodPost, "/collaborator/rpc/"+docID, auth,
		map[string]any{"method": "getContent", "payload": map[string]any{}})
	if code != http.StatusOK || string(body) != `{"content":{}}` {
		t.Fatalf("getContent without source = %d %s, want 200 {\"content\":{}}", code, body)
	}

	// createContent with an empty content map answers the same empty object.
	code, body = call(t, app, http.MethodPost, "/collaborator/rpc/"+docID, auth,
		map[string]any{"method": "createContent", "payload": map[string]any{"content": map[string]string{}}})
	if code != http.StatusOK || string(body) != `{"content":{}}` {
		t.Fatalf("createContent with no fields = %d %s, want 200 {\"content\":{}}", code, body)
	}

	// updateContent answers the bare empty object — no content key at all.
	code, body = call(t, app, http.MethodPost, "/collaborator/rpc/"+docID, auth,
		map[string]any{"method": "updateContent", "payload": map[string]any{"content": map[string]string{}}})
	if code != http.StatusOK || string(body) != `{}` {
		t.Fatalf("updateContent = %d %s, want 200 {}", code, body)
	}

	// An unknown verb is a SEMANTIC refusal, which this RPC reports under 200
	// because the client throws on result.error.
	code, body = call(t, app, http.MethodPost, "/collaborator/rpc/"+docID, auth,
		map[string]any{"method": "nope", "payload": map[string]any{}})
	if code != http.StatusOK || string(body) != `{"error":"unknown method nope"}` {
		t.Fatalf("unknown verb = %d %s, want 200 {\"error\":\"unknown method nope\"}", code, body)
	}

	// A malformed documentId is still a 400, and a request with no token at all
	// is still a 401 — the gates the op kept.
	if code, _ := call(t, app, http.MethodPost, "/collaborator/rpc/not-a-doc-id", auth,
		map[string]any{"method": "getContent", "payload": map[string]any{}}); code != http.StatusBadRequest {
		t.Fatalf("malformed documentId = %d, want 400", code)
	}
	if code, _ := call(t, app, http.MethodPost, "/collaborator/rpc/"+docID, nil,
		map[string]any{"method": "getContent", "payload": map[string]any{}}); code != http.StatusUnauthorized {
		t.Fatalf("no token = %d, want 401", code)
	}
}

// TestCollabRPCBridgedUnderBareMount is the regression bar for the defect typing
// this route surfaced: the collaborator plane is app-level, so team's own
// cloud.Bridge — scoped to the /v1/team group — never covered it. A typed op
// reaches its caller's token ONLY through the request cloud.Bridge parks, so
// without one on this plane every call would 401 under a bare Mount (the app's
// own tests, and any embedder that mounts without Serve). A 200 here proves the
// bridge is installed and precedes the op.
func TestCollabRPCBridgedUnderBareMount(t *testing.T) {
	app := mountTeam(t) // a bare zip.App: Mount only, no Serve, no fleet middleware
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	ws, err := mounted.State.accounts.EnsureWorkspace(t.Context(), org, acct, "Ada")
	if err != nil {
		t.Fatal(err)
	}
	docID := collabDocID(ws.UUID, "tracker:class:Issue", "issue-bridge", "description")
	code, body := call(t, app, http.MethodPost, "/collaborator/rpc/"+docID, bearerFor(t, acct, org),
		map[string]any{"method": "getContent", "payload": map[string]any{}})
	if code != http.StatusOK {
		t.Fatalf("bridged collab RPC = %d (%s), want 200 — cloud.Bridge missing from the /collaborator plane", code, body)
	}
}

// TestClearCookieIsExact pins the account-cookie DELETE: 200, {"result":true},
// and a Set-Cookie that EXPIRES the account token (max-age 0) — the sign-out the
// SPA depends on. It takes no body, which is what makes it a typed DELETE.
func TestClearCookieIsExact(t *testing.T) {
	app := mountTeam(t)
	req := httptest.NewRequest(http.MethodDelete, "/v1/team/account/cookie", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("DELETE /cookie = %d, want 200", resp.StatusCode)
	}
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	if got := string(buf[:n]); got != `{"result":true}` {
		t.Fatalf("DELETE /cookie body = %s, want {\"result\":true}", got)
	}
	sc := resp.Header.Get("Set-Cookie")
	if !strings.HasPrefix(sc, authCookie+"=;") || !strings.Contains(sc, "max-age=0") {
		t.Fatalf("DELETE /cookie Set-Cookie = %q, want an expiring %s", sc, authCookie)
	}
	if !strings.Contains(sc, "HttpOnly") || !strings.Contains(sc, "secure") {
		t.Fatalf("DELETE /cookie Set-Cookie = %q, want HttpOnly+Secure", sc)
	}
}

// TestClearCookieDegraded proves the typed DELETE carries the SAME fail-closed
// refusal Mount's guard gave it: a subsystem with no signing secret answers 503,
// because a typed op is not a zip.Handler and cannot be wrapped by that guard.
func TestClearCookieDegraded(t *testing.T) {
	t.Setenv("SERVER_SECRET", "")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), VFS: newMemVFS()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	if code, body := call(t, app, http.MethodDelete, "/v1/team/account/cookie", nil, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("degraded DELETE /cookie = %d (%s), want 503", code, body)
	}
}

// untypedByDesign is the CLOSED list of team operations that are NOT typed ops,
// each with the reason it cannot be one. A typed op is a route PLUS a registry
// entry — the one value the OpenAPI operation, the MCP tool, the CLI command and
// the SDK method all come from — so an operation missing from that registry is
// invisible to all four. These ten are missing on purpose. Addresses are written
// the way the DOCUMENT writes them, which is the identity every projection keys
// on.
var untypedByDesign = map[string]string{
	"GET /collaborator": "the response is a WebSocket upgrade (the live Y.js lane), not a value.",
	"GET /v1/team/transactor/{token}": "the response is a WebSocket upgrade (the transactor data " +
		"plane), not a value.",

	"POST /v1/team/account": "a JSON-RPC envelope: the verb is a body field, `result` is a different " +
		"shape per verb (LoginInfo, a workspace list, a bool, …), a refusal is HTTP 200 carrying " +
		"{error: Status} — INCLUDING for an unparseable body, which a typed In turns into a 400 — and the " +
		"entitlement arm answers 402 with a second key. A typed Out could only say `any`.",
	"PUT /v1/team/account/cookie": "a body this route cannot parse is IGNORED (the token falls back to " +
		"the Authorization bearer and the request succeeds) where a typed In answers 400.",
	"GET /v1/team/account/auth/{provider}": "a browser REDIRECT — 302 + Location + Set-Cookie, no body. " +
		"A typed op answers a JSON value under a 2xx.",
	"GET /v1/team/account/auth/{provider}/callback": "a browser REDIRECT back to the SPA — 302 + " +
		"Location + Set-Cookie, no body.",

	"GET /v1/team/billing/ui": "serves the embedded wallet page's BYTES (html/js/css) under a per-asset " +
		"Content-Type; a typed Out answers JSON.",
	"GET /v1/team/billing/ui/{wildcard1}": "serves the embedded wallet page's BYTES under a per-asset " +
		"Content-Type; a typed Out answers JSON.",

	"POST /v1/team/files/{workspace}": "the request is a MULTIPART form whose part filename IS the blob " +
		"id, which a typed In cannot describe — zip decodes a JSON body.",
	"GET /v1/team/files/{workspace}/{filename}": "the response is the blob's raw BYTES under a " +
		"byte-derived Content-Type; a typed Out answers JSON.",
}

// teamOps reads BOTH projections of the live router at their one shared address
// form: what the document says is served, and which of those carry a typed
// registry entry. Both team prefixes count — the collaborator plane is
// app-level, because the front derives it from COLLABORATOR_URL, and a route
// being outside /v1/team does not make it less of a product surface.
func teamOps(t *testing.T) (served map[string]bool, typed map[string]string) {
	t.Helper()
	app := mountTeam(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "team", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed registry: %v", err)
	}
	ours := func(p string) bool {
		return strings.HasPrefix(p, teamPrefix) || strings.HasPrefix(p, collabPrefix)
	}
	served, typed = map[string]bool{}, map[string]string{}
	for path, item := range doc.Paths {
		if !ours(path) {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key, op := range reg.Ops {
		if i := strings.Index(key, " "); i > 0 && ours(key[i+1:]) {
			typed[key] = op.Description
		}
	}
	return served, typed
}

// TestEveryRouteIsTypedOrNamed fails when a team operation is neither a typed op
// nor one of the ten above — so the next route added here is typed by default,
// and dropping one out of the registry takes a deliberate edit with a reason.
func TestEveryRouteIsTypedOrNamed(t *testing.T) {
	served, typed := teamOps(t)

	var untyped []string
	for key := range served {
		if _, ok := typed[key]; ok {
			continue
		}
		if _, named := untypedByDesign[key]; named {
			continue
		}
		untyped = append(untyped, key)
	}
	if len(untyped) > 0 {
		sort.Strings(untyped)
		t.Errorf("operation(s) with no registry entry and no reason: %s\n"+
			"A route that is not a typed op has no schema, no prose, no MCP tool, no CLI command and no SDK "+
			"method. Convert it (zip.Get/Post/... on the group), or add it to untypedByDesign with the reason "+
			"typing it would move the wire.", strings.Join(untyped, ", "))
	}
	// The reasons must describe operations that exist, or the list is stale prose.
	for key := range untypedByDesign {
		if !served[key] {
			t.Errorf("untypedByDesign names %q, which team no longer serves", key)
		}
	}
}

// Every typed op must carry lifted prose, because that prose IS the product
// surface: it becomes the OpenAPI description AND the MCP tool description a
// model reads to pick the tool. zipdoc_gen.go is what carries it into the
// binary, so an op added without regenerating shows up here as a nameless tool.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	_, typed := teamOps(t)
	if len(typed) == 0 {
		t.Fatal("no typed team ops in the registry at all")
	}
	for key, desc := range typed {
		if strings.TrimSpace(desc) == "" {
			t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/team/...", key)
		}
	}
}
