package team

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/team/token"
	"github.com/hanzoai/cloud/types"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

const testSecret = "team-http-test-secret"

// mountTeam mounts the team subsystem with an in-memory VFS so the files plane
// round-trips in tests.
func mountTeam(t *testing.T) *zip.App { return mountTeamVFS(t, newMemVFS()) }

// mountTeamVFS mounts the team subsystem onto a fresh zip.App with an isolated
// DataDir, a pinned (non-default) SERVER_SECRET so the subsystem is functional
// (not degraded), and the given VFS backend (memVFS for round-trips, or
// clients.DisabledVFS() to prove the files plane fails closed).
func mountTeamVFS(t *testing.T, vfs types.VFSClient) *zip.App {
	t.Helper()
	t.Setenv("SERVER_SECRET", testSecret)
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), VFS: vfs}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	return app
}

// call drives one request through the real Fiber stack. headers carries identity
// (X-Org-Id/X-User-Id/X-User-IsAdmin) and/or Authorization. A []byte body is sent
// RAW (octet-stream, for file uploads); any other non-nil body is JSON-encoded.
func call(t *testing.T, app *zip.App, method, path string, headers map[string]string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	contentType := ""
	switch b := body.(type) {
	case nil:
	case []byte:
		r = bytes.NewReader(b)
		contentType = "application/octet-stream"
	default:
		bs, _ := json.Marshal(b)
		r = bytes.NewReader(bs)
		contentType = "application/json"
	}
	req := httptest.NewRequest(method, path, r)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// TestSelectWorkspaceHTTP is the end-to-end account-RPC proof: a bearer-authed
// selectWorkspace resolves the caller's workspace, gates on membership, mints the
// workspace token and returns the transactor endpoint NAMESPACED under
// /v1/team/transactor (never bare /transactor).
func TestSelectWorkspaceHTTP(t *testing.T) {
	app := mountTeam(t)
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	ws, err := mounted.accounts.EnsureWorkspace(context.Background(), org, acct, "Ada")
	if err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	sess, err := token.Generate(acct, "", map[string]any{"org": org}, expUnix(sessionTokenTTL), testSecret)
	if err != nil {
		t.Fatalf("mint session token: %v", err)
	}
	auth := map[string]string{"Authorization": "Bearer " + sess}

	// getUserWorkspaces returns the seeded workspace.
	code, body := call(t, app, http.MethodPost, "/v1/team/account", auth, rpcRequest{Method: "getUserWorkspaces"})
	if code != http.StatusOK {
		t.Fatalf("getUserWorkspaces status %d: %s", code, body)
	}
	var wl struct {
		Result []WorkspaceInfo `json:"result"`
	}
	if err := json.Unmarshal(body, &wl); err != nil || len(wl.Result) != 1 || wl.Result[0].UUID != ws.UUID {
		t.Fatalf("getUserWorkspaces = %s (err %v)", body, err)
	}
	if wl.Result[0].VersionMajor != 0 || wl.Result[0].VersionMinor != 6 || wl.Result[0].VersionPatch != 0 {
		t.Fatalf("workspace version = %d.%d.%d, want 0.6.0", wl.Result[0].VersionMajor, wl.Result[0].VersionMinor, wl.Result[0].VersionPatch)
	}

	// selectWorkspace mints the workspace token + returns the transactor endpoint.
	code, body = call(t, app, http.MethodPost, "/v1/team/account", auth,
		map[string]any{"method": "selectWorkspace", "params": map[string]any{"workspaceUrl": ws.Slug}})
	if code != http.StatusOK {
		t.Fatalf("selectWorkspace status %d: %s", code, body)
	}
	var sw struct {
		Result WorkspaceLoginInfo `json:"result"`
		Error  *Status            `json:"error"`
	}
	if err := json.Unmarshal(body, &sw); err != nil {
		t.Fatalf("selectWorkspace decode: %v (%s)", err, body)
	}
	if sw.Error != nil {
		t.Fatalf("selectWorkspace error: %+v", sw.Error)
	}
	if sw.Result.Workspace != ws.UUID {
		t.Fatalf("workspace = %q, want %q", sw.Result.Workspace, ws.UUID)
	}
	if sw.Result.Role != "OWNER" {
		t.Fatalf("role = %q, want OWNER", sw.Result.Role)
	}
	const wantSuffix = "/v1/team/transactor"
	if got := sw.Result.Endpoint; len(got) < len(wantSuffix) || got[len(got)-len(wantSuffix):] != wantSuffix {
		t.Fatalf("endpoint = %q, must end with %q (namespaced under /v1/team)", got, wantSuffix)
	}

	// The returned workspace token decodes back to (acct, ws.UUID, org) under the
	// SAME secret the transactor verifies with — the wire is closed end-to-end.
	dec, err := token.Decode(sw.Result.Token, testSecret, true)
	if err != nil || dec.Account != acct || dec.Workspace != ws.UUID || dec.Extra["org"] != org {
		t.Fatalf("workspace token round-trip: %+v (err %v)", dec, err)
	}
}

// TestSelectWorkspaceCrossTenantBlocked proves a caller whose token org is org-b
// cannot select org-a's workspace by its slug — the store scopes the slug lookup
// by owner_org, so the RPC answers WorkspaceNotFound (no cross-tenant oracle).
func TestSelectWorkspaceCrossTenantBlocked(t *testing.T) {
	app := mountTeam(t)
	ctx := context.Background()
	const acctA = "aaaaaaaa-0000-4000-8000-00000000000a"
	const acctB = "bbbbbbbb-0000-4000-8000-00000000000b"
	wsA, _ := mounted.accounts.EnsureWorkspace(ctx, "org-a", acctA, "Alice")
	if _, err := mounted.accounts.EnsureWorkspace(ctx, "org-b", acctB, "Bob"); err != nil {
		t.Fatal(err)
	}

	// org-b's caller presents org-a's slug.
	tokB, _ := token.Generate(acctB, "", map[string]any{"org": "org-b"}, expUnix(sessionTokenTTL), testSecret)
	code, body := call(t, app, http.MethodPost, "/v1/team/account",
		map[string]string{"Authorization": "Bearer " + tokB},
		map[string]any{"method": "selectWorkspace", "params": map[string]any{"workspaceUrl": wsA.Slug}})
	if code != http.StatusOK {
		t.Fatalf("status %d: %s", code, body)
	}
	var sw struct {
		Result *WorkspaceLoginInfo `json:"result"`
		Error  *Status             `json:"error"`
	}
	_ = json.Unmarshal(body, &sw)
	if sw.Result != nil && sw.Result.Workspace != "" {
		t.Fatalf("cross-tenant selectWorkspace leaked a workspace: %+v", sw.Result)
	}
	if sw.Error == nil || sw.Error.Code != "account:status:WorkspaceNotFound" {
		t.Fatalf("cross-tenant selectWorkspace = %s, want WorkspaceNotFound", body)
	}
}

// TestAccountRPCRequiresToken proves an unauthenticated RPC is refused (the
// account plane never serves a principal it did not mint a token for).
func TestAccountRPCRequiresToken(t *testing.T) {
	app := mountTeam(t)
	code, body := call(t, app, http.MethodPost, "/v1/team/account", nil, rpcRequest{Method: "getUserWorkspaces"})
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	var r struct {
		Error *Status `json:"error"`
	}
	_ = json.Unmarshal(body, &r)
	if r.Error == nil || r.Error.Code != "account:status:Unauthorized" {
		t.Fatalf("no-token RPC = %s, want Unauthorized", body)
	}
}

// TestBotsReadRouteTenantGate is the red bar for the bots surface: GET /v1/team/bots
// requires a VALIDATED principal (X-User-Id set by the identity middleware) — a
// bare X-Org-Id is refused, and with a validated org the list is org-scoped (empty
// here since the agents subsystem is not mounted in this test).
func TestBotsReadRouteTenantGate(t *testing.T) {
	app := mountTeam(t)

	// No principal at all → 403.
	if code, _ := call(t, app, http.MethodGet, "/v1/team/bots", nil, nil); code != http.StatusForbidden {
		t.Fatalf("no-principal GET /v1/team/bots = %d, want 403", code)
	}
	// A client-forged X-Org-Id with NO validated X-User-Id → still 403 (the exact
	// off-gateway forge principal.Tenant refuses).
	if code, _ := call(t, app, http.MethodGet, "/v1/team/bots", map[string]string{"X-Org-Id": "victim"}, nil); code != http.StatusForbidden {
		t.Fatalf("forged-org GET /v1/team/bots = %d, want 403", code)
	}
	// A validated principal → 200 with an (empty) org-scoped list.
	code, body := call(t, app, http.MethodGet, "/v1/team/bots",
		map[string]string{"X-Org-Id": "acme", "X-User-Id": "u_acme"}, nil)
	if code != http.StatusOK {
		t.Fatalf("validated GET /v1/team/bots = %d: %s", code, body)
	}
	var lr struct {
		Bots []botView `json:"bots"`
	}
	if err := json.Unmarshal(body, &lr); err != nil {
		t.Fatalf("bots list decode: %v (%s)", err, body)
	}
	if len(lr.Bots) != 0 {
		t.Fatalf("bots = %d, want 0 (agents not mounted in test)", len(lr.Bots))
	}
}

// TestDegradedWithoutSecret is the Red CRITICAL guard: with SERVER_SECRET unset
// (and no dev escape hatch) Mount still SUCCEEDS (the cloud binary + all other
// subsystems stay up), but every /v1/team route fails closed with 503 and NO token
// is ever decoded — so a forged token is useless. A valid secret enables the
// routes. (The uniform /v1/team/health probe is owned by serve.go and stays 200; it
// is not registered by Mount, so this unit test asserts the route-degrade, which is
// the security-load-bearing half.)
func TestDegradedWithoutSecret(t *testing.T) {
	t.Setenv("SERVER_SECRET", "")     // unset
	t.Setenv("TEAM_DEV_INSECURE", "") // no escape hatch
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), VFS: newMemVFS()}); err != nil {
		t.Fatalf("Mount must SUCCEED in degraded mode (health-only), got: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })

	// A validated principal that would otherwise 200 now gets 503 — fail closed.
	code, body := call(t, app, http.MethodGet, "/v1/team/bots",
		map[string]string{"X-Org-Id": "acme", "X-User-Id": "u_acme"}, nil)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("degraded /v1/team/bots = %d, want 503 (%s)", code, body)
	}
	// The account RPC is 503 too (no token decoded/accepted while degraded).
	if code, _ := call(t, app, http.MethodPost, "/v1/team/account", nil, rpcRequest{Method: "getUserWorkspaces"}); code != http.StatusServiceUnavailable {
		t.Fatalf("degraded /v1/team/account = %d, want 503", code)
	}
	// The default-secret literal is ALSO degraded (a public key must never sign).
	_ = Shutdown()
	t.Setenv("SERVER_SECRET", "secret") // == token.DefaultSecret
	app2 := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app2, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), VFS: newMemVFS()}); err != nil {
		t.Fatalf("Mount (default secret) must succeed degraded: %v", err)
	}
	if code, _ := call(t, app2, http.MethodGet, "/v1/team/bots", map[string]string{"X-Org-Id": "acme", "X-User-Id": "u_acme"}, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("default-secret /v1/team/bots = %d, want 503 (public key must not sign)", code)
	}
}

// TestDevInsecureEnablesRoutes proves the explicit dev escape hatch
// (TEAM_DEV_INSECURE=1) runs functional on the default secret — for local dev
// only. The route is NOT degraded.
func TestDevInsecureEnablesRoutes(t *testing.T) {
	t.Setenv("SERVER_SECRET", "")
	t.Setenv("TEAM_DEV_INSECURE", "1")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), VFS: newMemVFS()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	// Functional: a validated principal gets 200 (empty list), not 503.
	if code, body := call(t, app, http.MethodGet, "/v1/team/bots", map[string]string{"X-Org-Id": "acme", "X-User-Id": "u_acme"}, nil); code != http.StatusOK {
		t.Fatalf("dev-insecure /v1/team/bots = %d, want 200 (%s)", code, body)
	}
}

// TestSetCookieRejectsBadToken is the fix for the login-CSRF/fixation vector:
// PUT /cookie must verify the token before persisting it, so a caller-supplied
// garbage/forged token is refused (401) and never becomes a session cookie.
func TestSetCookieRejectsBadToken(t *testing.T) {
	app := mountTeam(t)
	// A verifiable token this service signed → accepted.
	good, _ := token.Generate("550e8400-e29b-41d4-a716-446655440000", "", map[string]any{"org": "acme"}, expUnix(sessionTokenTTL), testSecret)
	if code, _ := call(t, app, http.MethodPut, "/v1/team/account/cookie", nil, map[string]any{"token": good}); code != http.StatusOK {
		t.Fatalf("valid token PUT /cookie = %d, want 200", code)
	}
	// Garbage / forged token → rejected, never stored.
	if code, _ := call(t, app, http.MethodPut, "/v1/team/account/cookie", nil, map[string]any{"token": "not.a.token"}); code != http.StatusUnauthorized {
		t.Fatalf("bad token PUT /cookie = %d, want 401", code)
	}
	forged, _ := token.Generate("550e8400-e29b-41d4-a716-446655440000", "", map[string]any{"org": "acme"}, expUnix(sessionTokenTTL), "attacker-secret")
	if code, _ := call(t, app, http.MethodPut, "/v1/team/account/cookie", nil, map[string]any{"token": forged}); code != http.StatusUnauthorized {
		t.Fatalf("foreign-signed token PUT /cookie = %d, want 401", code)
	}
}

// TestBotsSyncAdminGate proves POST /v1/team/bots/sync requires BOTH a validated
// org AND the gateway-minted admin flag — a validated non-admin is refused, and an
// admin gets a clean (0-projected, no agents) response.
func TestBotsSyncAdminGate(t *testing.T) {
	app := mountTeam(t)

	// Validated but NOT admin → 403.
	code, _ := call(t, app, http.MethodPost, "/v1/team/bots/sync",
		map[string]string{"X-Org-Id": "acme", "X-User-Id": "u_acme"}, nil)
	if code != http.StatusForbidden {
		t.Fatalf("non-admin sync = %d, want 403", code)
	}
	// Validated admin → 200.
	code, body := call(t, app, http.MethodPost, "/v1/team/bots/sync",
		map[string]string{"X-Org-Id": "acme", "X-User-Id": "u_acme", "X-User-IsAdmin": "true"}, nil)
	if code != http.StatusOK {
		t.Fatalf("admin sync = %d: %s", code, body)
	}
	var sr struct {
		Synced    bool `json:"synced"`
		Projected int  `json:"projected"`
	}
	if err := json.Unmarshal(body, &sr); err != nil || !sr.Synced {
		t.Fatalf("admin sync body = %s (err %v)", body, err)
	}
}
