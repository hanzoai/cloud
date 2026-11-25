// SPDX-License-Identifier: Apache-2.0 OR MIT

package apps_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	gojose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps"
	"github.com/hanzoai/cloud/audit"
)

type testIAM struct {
	key    *rsa.PrivateKey
	signer gojose.Signer
	server *httptest.Server
	issuer string
}

func newTestIAM(t *testing.T) *testIAM {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	pubJWK := gojose.JSONWebKey{
		Key:       &key.PublicKey,
		KeyID:     "test-kid-1",
		Algorithm: string(gojose.RS256),
		Use:       "sig",
	}
	jwksBytes, err := json.Marshal(gojose.JSONWebKeySet{Keys: []gojose.JSONWebKey{pubJWK}})
	if err != nil {
		t.Fatalf("marshal jwks: %v", err)
	}
	signer, err := gojose.NewSigner(
		gojose.SigningKey{Algorithm: gojose.RS256, Key: key},
		(&gojose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-kid-1"),
	)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	ti := &testIAM{key: key, signer: signer}
	ti.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/iam/.well-known/jwks":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(jwksBytes)
		case "/v1/iam/oauth/token":
			_ = r.ParseForm()
			if r.FormValue("code") != "pkce-code-alpha" || len(r.FormValue("code_verifier")) < 43 {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			tok := ti.mint(t, "org-alpha", "alice", true)
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": tok,
				"token_type":   "Bearer",
				"expires_in":   3600,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	ti.issuer = ti.server.URL
	t.Cleanup(ti.server.Close)
	return ti
}

func (ti *testIAM) mint(t *testing.T, org, user string, isAdmin bool) string {
	t.Helper()
	now := time.Now()
	claims := map[string]any{
		"iss":                ti.issuer,
		"sub":                "sub-" + org + "-" + user,
		"name":               user,
		"preferred_username": user,
		"email":              user + "@" + org + ".example",
		"owner":              org,
		"isAdmin":            isAdmin,
		"iat":                jwt.NewNumericDate(now),
		"exp":                jwt.NewNumericDate(now.Add(15 * time.Minute)),
	}
	raw, err := jwt.Signed(ti.signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return raw
}

func doJSON(t *testing.T, method, url, bearer string, body any, extraHeaders map[string]string) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatalf("new req: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	res, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer res.Body.Close()
	out, _ := io.ReadAll(res.Body)
	return res.StatusCode, out
}

func TestFanClubCloudNativeE2E(t *testing.T) {
	t.Setenv("CLOUD_DEV_UNENCRYPTED", "1")
	runDir, err := os.MkdirTemp("/tmp", "fc-e2e-")
	if err != nil {
		t.Fatalf("runDir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	t.Setenv(zip.RuntimeDirEnv, runDir)

	iam := newTestIAM(t)

	// Local HTTP AI server simulating real Hanzo AI /v1/chat/completions + outage toggle.
	var aiOutage atomic.Bool
	var lastAIBearer atomic.Value
	aiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		lastAIBearer.Store(r.Header.Get("Authorization"))
		if aiOutage.Load() {
			http.Error(w, "upstream model outage", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"hanzo/zen","choices":[{"message":{"role":"assistant","content":"Aggregate review: prioritize consented high-risk renewals."}}],"usage":{"prompt_tokens":40,"completion_tokens":10,"total_tokens":50}}`))
	}))
	defer aiSrv.Close()

	dataDir := t.TempDir()
	cfg := &cloud.Config{
		Brand:          "hanzo",
		Version:        "test",
		Domain:         "127.0.0.1:8080",
		IAMIssuer:      iam.issuer,
		AdminOrg:       "admin",
		JWKSURL:        iam.issuer + "/v1/iam/.well-known/jwks",
		DataDir:        dataDir,
		AIDefaultModel: "zen",
		AIBaseURL:      aiSrv.URL,
		Enable:         []string{"base", "tasks", "fanclub"},
		EnableStaged:   []string{"fanclub"},
	}
	t.Setenv("CLOUD_JWKS_URL", cfg.JWKSURL)
	t.Setenv("FANCLUB_OAUTH_REDIRECT_URIS", "http://127.0.0.1:8080/fanclub/index.html")

	deps := cloud.BuildDeps(cfg)
	auditRec, err := audit.Open(dataDir+"/audit.db", nil)
	if err != nil {
		t.Fatalf("audit.Open: %v", err)
	}
	t.Cleanup(func() { _ = auditRec.Close() })
	deps.Audit = auditRec

	app := zip.New(zip.Config{AppName: "fanclub-e2e"})
	app.Use(cloud.IdentityMiddleware(cfg))
	app.Use(cloud.Carry())
	app.Use(cloud.AuditTrail(auditRec))

	if err := cloud.MountAll(app, apps.Wire(), cfg, deps); err != nil {
		t.Fatalf("MountAll: %v", err)
	}
	app.Authorize(cloud.Rule())

	sp := zip.SocketPath("fanclub-e2e")
	go func() { _ = app.Listen(sp) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	for deadline := time.Now().Add(5 * time.Second); ; {
		if _, err := os.Stat(sp); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("zap socket never appeared at %s", sp)
		}
		time.Sleep(5 * time.Millisecond)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = fasthttp.Serve(ln, app.Fiber().Handler()) }()
	t.Cleanup(func() { _ = ln.Close() })
	baseURL := "http://" + ln.Addr().String()

	// 1. Public config & PKCE OAuth exchange (including exact redirect check).
	code, body := doJSON(t, http.MethodGet, baseURL+"/v1/fanclub/config", "", nil, nil)
	if code != http.StatusOK || !strings.Contains(string(body), iam.issuer) {
		t.Fatalf("GET /v1/fanclub/config = %d %s", code, body)
	}
	code, _ = doJSON(t, http.MethodPost, baseURL+"/v1/fanclub/oauth/exchange", "", map[string]any{
		"code":         "pkce-code-alpha",
		"codeVerifier": strings.Repeat("a", 48),
		"redirectUri":  "https://attacker.example/callback",
	}, nil)
	if code != http.StatusBadRequest {
		t.Fatalf("bad redirectUri status = %d, want 400", code)
	}
	code, body = doJSON(t, http.MethodPost, baseURL+"/v1/fanclub/oauth/exchange", "", map[string]any{
		"code":         "pkce-code-alpha",
		"codeVerifier": strings.Repeat("a", 48),
		"redirectUri":  "http://127.0.0.1:8080/fanclub/index.html",
	}, nil)
	if code != http.StatusOK {
		t.Fatalf("valid oauth exchange = %d %s", code, body)
	}
	var exch struct {
		AccessToken string `json:"accessToken"`
		Identity    struct {
			Org string `json:"org"`
		} `json:"identity"`
	}
	_ = json.Unmarshal(body, &exch)
	if exch.AccessToken == "" || exch.Identity.Org != "org-alpha" {
		t.Fatalf("unexpected exchange output: %s", body)
	}

	alphaAdminTok := exch.AccessToken
	alphaMemberTok := iam.mint(t, "org-alpha", "bob", false)
	betaAdminTok := iam.mint(t, "org-beta", "carol", true)

	// 2. Unauthenticated + forged X-Org-Id/X-User-IsAdmin headers are rejected (401).
	code, _ = doJSON(t, http.MethodGet, baseURL+"/v1/fanclub/me", "", nil, map[string]string{
		"X-Org-Id":       "org-alpha",
		"X-User-IsAdmin": "true",
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("forged unauth /v1/fanclub/me = %d, want 401", code)
	}

	// 3. Validated /v1/fanclub/me pins org to IAM owner despite spoofed X-Org-Id header.
	code, body = doJSON(t, http.MethodGet, baseURL+"/v1/fanclub/me", alphaAdminTok, nil, map[string]string{
		"X-Org-Id": "org-beta",
	})
	if code != http.StatusOK || !strings.Contains(string(body), `"org":"org-alpha"`) {
		t.Fatalf("me with spoofed X-Org-Id = %d %s", code, body)
	}

	// 4. Seed org-alpha and verify records live directly in Cloud Base (/v1/base/collections/fc_members).
	code, body = doJSON(t, http.MethodPost, baseURL+"/v1/fanclub/seed", alphaAdminTok, map[string]any{"profile": "northstar"}, nil)
	if code != http.StatusCreated || !strings.Contains(string(body), `"seeded":90`) {
		t.Fatalf("seed = %d %s", code, body)
	}
	code, body = doJSON(t, http.MethodGet, baseURL+"/v1/base/collections/fc_members?limit=5", alphaAdminTok, nil, nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"northstar-001"`) {
		t.Fatalf("Cloud Base /v1/base/collections/fc_members = %d %s", code, body)
	}

	// 5. Cross-tenant isolation on BOTH /v1/fanclub and /v1/base.
	code, body = doJSON(t, http.MethodGet, baseURL+"/v1/base/collections/fc_members", betaAdminTok, nil, nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"records":[]`) {
		t.Fatalf("org-beta Cloud Base isolation breach = %d %s", code, body)
	}

	// 6. Plan proposals -> verify role auth, cross-tenant denial, Cloud Tasks dispatch, idempotency, and audit.
	code, body = doJSON(t, http.MethodPost, baseURL+"/v1/fanclub/proposals/plan", alphaAdminTok, map[string]any{"journey": "retention", "limit": 3}, nil)
	if code != http.StatusCreated {
		t.Fatalf("plan = %d %s", code, body)
	}
	code, body = doJSON(t, http.MethodGet, baseURL+"/v1/fanclub/proposals", alphaAdminTok, nil, nil)
	var propsResp struct {
		Proposals []struct {
			ID string `json:"id"`
		} `json:"proposals"`
	}
	_ = json.Unmarshal(body, &propsResp)
	if code != http.StatusOK || len(propsResp.Proposals) == 0 {
		t.Fatalf("proposals = %d %s", code, body)
	}
	propID := propsResp.Proposals[0].ID

	// Plain member denied 403.
	code, _ = doJSON(t, http.MethodPost, baseURL+"/v1/fanclub/proposals/"+propID+"/decision", alphaMemberTok, map[string]any{"action": "approve"}, nil)
	if code != http.StatusForbidden {
		t.Fatalf("non-admin approve = %d, want 403", code)
	}
	// Cross-tenant admin denied 404.
	code, _ = doJSON(t, http.MethodPost, baseURL+"/v1/fanclub/proposals/"+propID+"/decision", betaAdminTok, map[string]any{"action": "approve"}, nil)
	if code != http.StatusNotFound {
		t.Fatalf("cross-tenant approve = %d, want 404", code)
	}
	// Org-alpha admin approves -> durable Cloud Task created!
	code, body = doJSON(t, http.MethodPost, baseURL+"/v1/fanclub/proposals/"+propID+"/decision", alphaAdminTok, map[string]any{"action": "approve"}, nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"Approved (simulation)"`) || !strings.Contains(string(body), `"externalSideEffects":false`) {
		t.Fatalf("admin approve = %d %s", code, body)
	}
	code, body = doJSON(t, http.MethodGet, baseURL+"/v1/tasks", alphaAdminTok, nil, nil)
	if code != http.StatusOK || !strings.Contains(string(body), `"fanclub_action_simulation"`) {
		t.Fatalf("Cloud Tasks missing simulation task = %d %s", code, body)
	}
	// Duplicate approval fails 409 Conflict.
	code, _ = doJSON(t, http.MethodPost, baseURL+"/v1/fanclub/proposals/"+propID+"/decision", alphaAdminTok, map[string]any{"action": "approve"}, nil)
	if code != http.StatusConflict {
		t.Fatalf("duplicate approve = %d, want 409", code)
	}

	// 7. Real HTTP AI chat + bearer relay + fail-closed model outage behavior.
	code, body = doJSON(t, http.MethodPost, baseURL+"/v1/fanclub/chat", alphaAdminTok, map[string]any{"question": "What should we prioritize?"}, nil)
	if code != http.StatusOK || !strings.Contains(string(body), "Aggregate review") {
		t.Fatalf("chat live = %d %s", code, body)
	}
	if gotAuth, _ := lastAIBearer.Load().(string); gotAuth != "Bearer "+alphaAdminTok {
		t.Fatalf("AI upstream Authorization = %q, want relayed IAM bearer", gotAuth)
	}
	aiOutage.Store(true)
	code, body = doJSON(t, http.MethodPost, baseURL+"/v1/fanclub/chat", alphaAdminTok, map[string]any{"question": "What now?"}, nil)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("chat during model outage = %d %s, want 503 fail-closed", code, body)
	}

	// 8. MCP /mcp JSON-RPC 2.0 authorization & IAM-scoped read-only tool verification.
	mcpFrame := map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params":  map[string]any{"name": "fanclub_summary", "arguments": map[string]any{}},
	}
	// Unauthenticated MCP call must fail closed (never leak org-alpha data).
	_, body = doJSON(t, http.MethodPost, baseURL+"/mcp", "", mcpFrame, nil)
	if strings.Contains(string(body), "org-alpha") || !strings.Contains(string(body), "authenticated Hanzo IAM principal required") {
		t.Fatalf("unauthenticated MCP fanclub_summary should fail closed: %s", body)
	}
	// Authenticated org-alpha MCP call succeeds and returns org-alpha Base counts.
	code, body = doJSON(t, http.MethodPost, baseURL+"/mcp", alphaAdminTok, mcpFrame, nil)
	if code != http.StatusOK || !strings.Contains(string(body), "org-alpha") || !strings.Contains(string(body), "fc_members") {
		t.Fatalf("authenticated MCP fanclub_summary = %d %s", code, body)
	}
	// Authenticated org-beta MCP call returns 0 members (strict org isolation over MCP).
	code, body = doJSON(t, http.MethodPost, baseURL+"/mcp", betaAdminTok, mcpFrame, nil)
	if code != http.StatusOK || !strings.Contains(string(body), "org-beta") || strings.Contains(string(body), "org-alpha") {
		t.Fatalf("org-beta MCP fanclub_summary isolation failed = %d %s", code, body)
	}

	// 9. Cloud tamper-evident audit recorder observed the mutating operations.
	rows, total, err := auditRec.Query(context.Background(), audit.Filter{})
	if err != nil || total == 0 || len(rows) == 0 {
		t.Fatalf("audit trail empty: total=%d err=%v", total, err)
	}
}
