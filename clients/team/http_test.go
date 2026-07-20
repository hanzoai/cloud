package team

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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

// authStartRedirectURI drives GET /v1/team/account/auth/openid with a chosen
// request Host and returns the redirect_uri the 302 carries (the OAuth callback
// cloud asks IAM to bounce back to).
func authStartRedirectURI(t *testing.T, app *zip.App, host string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "http://"+host+"/v1/team/account/auth/openid", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("authStart Test: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("authStart status = %d, want 302", resp.StatusCode)
	}
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("bad Location %q: %v", resp.Header.Get("Location"), err)
	}
	return loc.Query().Get("redirect_uri")
}

// TestAuthStartRedirectURIHonorsPublicURL is the v.98 fix: with TEAM_PUBLIC_URL
// set, the OAuth redirect_uri is the PUBLIC callback REGARDLESS of the request
// Host — so behind the gateway (Host rewritten to the internal cluster host) cloud
// still emits the callback IAM accepts, and Dave's login works.
func TestAuthStartRedirectURIHonorsPublicURL(t *testing.T) {
	t.Setenv("TEAM_PUBLIC_URL", "https://hanzo.team")
	app := mountTeam(t)
	// A request whose Host is the INTERNAL cluster host (what the gateway forwards).
	got := authStartRedirectURI(t, app, "cloud.hanzo.svc.cluster.local:8000")
	if want := "https://hanzo.team/v1/team/account/auth/openid/callback"; got != want {
		t.Fatalf("redirect_uri = %q, want %q (public URL wins, Host ignored)", got, want)
	}
}

// TestAuthStartRedirectURIFallsBackToHost proves NO regression when TEAM_PUBLIC_URL
// is unset: the redirect_uri follows the request Host (originOf) — the direct-route
// and every other deployment are unchanged.
func TestAuthStartRedirectURIFallsBackToHost(t *testing.T) {
	t.Setenv("TEAM_PUBLIC_URL", "")
	t.Setenv("PUBLIC_ORIGIN", "")
	app := mountTeam(t)
	got := authStartRedirectURI(t, app, "hanzo.team")
	if want := "https://hanzo.team/v1/team/account/auth/openid/callback"; got != want {
		t.Fatalf("redirect_uri = %q, want %q (follows request Host)", got, want)
	}
	// And a DIFFERENT host is reflected verbatim (proves it is host-derived).
	got2 := authStartRedirectURI(t, app, "cloud.hanzo.svc.cluster.local:8000")
	if want := "https://cloud.hanzo.svc.cluster.local:8000/v1/team/account/auth/openid/callback"; got2 != want {
		t.Fatalf("redirect_uri = %q, want %q (host-derived)", got2, want)
	}
}

// TestSelectWorkspaceHTTP is the end-to-end account-RPC proof: a bearer-authed
// selectWorkspace resolves the caller's workspace, gates on membership, mints the
// workspace token and returns the transactor endpoint NAMESPACED under
// /v1/team/transactor (never bare /transactor).
func TestSelectWorkspaceHTTP(t *testing.T) {
	app := mountTeam(t)
	const org, acct = "acme", "550e8400-e29b-41d4-a716-446655440000"
	ws, err := mounted.State.accounts.EnsureWorkspace(context.Background(), org, acct, "Ada")
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
	wsA, _ := mounted.State.accounts.EnsureWorkspace(ctx, "org-a", acctA, "Alice")
	if _, err := mounted.State.accounts.EnsureWorkspace(ctx, "org-b", acctB, "Bob"); err != nil {
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
	// off-gateway forge principal.Org refuses).
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
	t.Setenv("SERVER_SECRET", "") // unset
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
	// The upstream public "secret" literal is ALSO degraded (a public key must
	// never sign) — and there is NO env escape hatch back to it.
	_ = Shutdown()
	t.Setenv("SERVER_SECRET", "secret")
	app2 := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app2, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), VFS: newMemVFS()}); err != nil {
		t.Fatalf("Mount (default secret) must succeed degraded: %v", err)
	}
	if code, _ := call(t, app2, http.MethodGet, "/v1/team/bots", map[string]string{"X-Org-Id": "acme", "X-User-Id": "u_acme"}, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("default-secret /v1/team/bots = %d, want 503 (public key must not sign)", code)
	}
}

// TestInsecureHatchRemoved proves the retired TEAM_DEV_INSECURE escape hatch is
// DEAD: even with it set, an unset SERVER_SECRET stays degraded. Dev runs on a
// real secret like every other deployment — one way.
func TestInsecureHatchRemoved(t *testing.T) {
	t.Setenv("SERVER_SECRET", "")
	t.Setenv("TEAM_DEV_INSECURE", "1")
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), VFS: newMemVFS()}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	if code, body := call(t, app, http.MethodGet, "/v1/team/bots", map[string]string{"X-Org-Id": "acme", "X-User-Id": "u_acme"}, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("/v1/team/bots with dead hatch = %d, want 503 (%s)", code, body)
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

// TestOAuthStateRoundTrip: authStart mints a RANDOM state nonce bound to the
// flow cookie (the navigateUrl rides in the cookie, not in state), and the
// callback refuses any state that does not match — no cookie, wrong nonce —
// before ever touching the code exchange.
func TestOAuthStateRoundTrip(t *testing.T) {
	app := mountTeam(t)

	req := httptest.NewRequest(http.MethodGet, "https://hanzo.team/v1/team/account/auth/openid?navigateUrl=/inbox", nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("authStart: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil {
		t.Fatalf("Location: %v", err)
	}
	state := loc.Query().Get("state")
	if len(state) != 32 {
		t.Fatalf("state = %q, want a 32-hex nonce (never the navigateUrl)", state)
	}
	var flow string
	for _, ck := range resp.Cookies() {
		if ck.Name == stateCookie {
			flow = ck.Value
		}
	}
	wantCookie := state + "|" + url.QueryEscape("/inbox")
	if flow != wantCookie {
		t.Fatalf("flow cookie = %q, want %q", flow, wantCookie)
	}

	// Callback with a MISMATCHED state (cookie present) → bounced, never exchanged.
	cb := httptest.NewRequest(http.MethodGet, "https://hanzo.team/v1/team/account/auth/openid/callback?code=x&state=forged", nil)
	cb.AddCookie(&http.Cookie{Name: stateCookie, Value: flow})
	resp2, err := app.Fiber().Test(cb)
	if err != nil {
		t.Fatalf("callback: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if loc2 := resp2.Header.Get("Location"); !strings.Contains(loc2, "error=state_mismatch") {
		t.Fatalf("mismatched state Location = %q, want error=state_mismatch", loc2)
	}

	// Callback with NO flow cookie → same refusal.
	cb2 := httptest.NewRequest(http.MethodGet, "https://hanzo.team/v1/team/account/auth/openid/callback?code=x&state="+state, nil)
	resp3, err := app.Fiber().Test(cb2)
	if err != nil {
		t.Fatalf("callback (no cookie): %v", err)
	}
	defer func() { _ = resp3.Body.Close() }()
	if loc3 := resp3.Header.Get("Location"); !strings.Contains(loc3, "error=state_mismatch") {
		t.Fatalf("cookieless callback Location = %q, want error=state_mismatch", loc3)
	}
}

// TestCallbackVerifiesOwner drives the FULL callback against a stub IAM: the
// tenant comes from the VERIFIED owner (the injected validator), and a token
// that fails verification fails CLOSED (error bounce, no default org, no
// session minted).
func TestCallbackVerifiesOwner(t *testing.T) {
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/iam/oauth/token":
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "iam-access"})
		case "/v1/iam/oauth/userinfo":
			_ = json.NewEncoder(w).Encode(map[string]string{"sub": "550e8400-e29b-41d4-a716-446655440000", "name": "Ada"})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer iam.Close()

	drive := func(verify func(string) (cloud.VerifiedIdentity, error)) *http.Response {
		store, err := openAccountStore(t.TempDir() + "/account.db")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		g := &api{
			accounts: store,
			cfg:      config{serverSecret: testSecret, iamEndpoint: iam.URL, iamClientID: "hanzo-team", provider: "openid"},
			log:      luxlog.New("test"),
			verify:   verify,
		}
		app := zip.New(zip.Config{Logger: luxlog.New("test")})
		g.register(app.Group("/v1/team"), func(h zip.Handler) zip.Handler { return h })
		cb := httptest.NewRequest(http.MethodGet, "https://hanzo.team/v1/team/account/auth/openid/callback?code=ok&state=aaaa", nil)
		cb.AddCookie(&http.Cookie{Name: stateCookie, Value: "aaaa|"})
		resp, err := app.Fiber().Test(cb)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = resp.Body.Close() })
		return resp
	}

	// Verified owner → session token minted, carrying the VERIFIED org.
	resp := drive(func(access string) (cloud.VerifiedIdentity, error) {
		if access != "iam-access" {
			t.Fatalf("verify saw %q, want the exchanged access token", access)
		}
		return cloud.VerifiedIdentity{Owner: "acme"}, nil
	})
	loc, _ := url.Parse(resp.Header.Get("Location"))
	sess := loc.Query().Get("token")
	if sess == "" {
		t.Fatalf("no session token in bounce: %q", resp.Header.Get("Location"))
	}
	dec, err := token.Decode(sess, testSecret, true)
	if err != nil || dec.Extra["org"] != "acme" {
		t.Fatalf("session token org = %v (err %v), want verified owner acme", dec, err)
	}

	// Verification failure → fail closed: error bounce, NO token.
	resp2 := drive(func(string) (cloud.VerifiedIdentity, error) {
		return cloud.VerifiedIdentity{}, fmt.Errorf("signature mismatch")
	})
	loc2 := resp2.Header.Get("Location")
	if !strings.Contains(loc2, "error=org_failed") || strings.Contains(loc2, "token=") {
		t.Fatalf("unverified owner Location = %q, want error=org_failed and no token", loc2)
	}
}

// TestTransactorStatistics: the workspace switcher's poll target answers the
// front-compatible shape under a valid token and 401s otherwise.
func TestTransactorStatistics(t *testing.T) {
	app := mountTeam(t)
	const acct, wsUUID = "550e8400-e29b-41d4-a716-446655440000", "6ba7b810-9dad-11d1-80b4-00c04fd430c8"
	wsTok, err := token.Generate(acct, wsUUID, map[string]any{"org": "acme"}, expUnix(workspaceTokenTTL), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	code, body := call(t, app, http.MethodGet, "/v1/team/transactor/api/v1/statistics?token="+wsTok, nil, nil)
	if code != http.StatusOK {
		t.Fatalf("statistics = %d, want 200 (%s)", code, body)
	}
	var out struct {
		Metrics    map[string]any `json:"metrics"`
		Statistics struct {
			ActiveSessions map[string][]map[string]any `json:"activeSessions"`
		} `json:"statistics"`
		Admin *bool `json:"admin"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("shape: %v (%s)", err, body)
	}
	if out.Metrics == nil || out.Statistics.ActiveSessions == nil || out.Admin == nil {
		t.Fatalf("missing keys: %s", body)
	}
	if _, ok := out.Statistics.ActiveSessions[wsUUID]; !ok {
		t.Fatalf("activeSessions missing the token's workspace: %s", body)
	}
	if code, _ := call(t, app, http.MethodGet, "/v1/team/transactor/api/v1/statistics?token=not.a.token", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("bad token statistics = %d, want 401", code)
	}
}
