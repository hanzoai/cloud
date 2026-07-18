package integrations

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/cek"
	"github.com/hanzoai/cloud/clients/kms"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// The cek data plane fail-closes without a master key on encryption-capable
// builds; supply one process-wide so integrations.db opens in local runs
// exactly as it does in CI (mirrors clients/git, clients/flags).
func TestMain(m *testing.M) {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		panic(err)
	}
	cek.SetMasterKey(k)
	os.Exit(m.Run())
}

// newKMS builds a REAL clients/kms.Client with a fresh 32-byte master key — no
// mock: token custody is exercised through the exact seal/open path production
// uses.
func newKMS(t *testing.T) *kms.Client {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("rand: %v", err)
	}
	kc, err := kms.New(kms.Config{
		DataDir:      t.TempDir(),
		MasterKeyB64: base64.StdEncoding.EncodeToString(key),
	}, luxlog.New("test"))
	if err != nil {
		t.Fatalf("kms.New: %v", err)
	}
	if !kc.Ready() {
		t.Fatal("test kms must be Ready with a 32-byte key")
	}
	t.Cleanup(func() { _ = kc.Close() })
	return kc
}

func newApp(t *testing.T, kc *kms.Client) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir(), Domain: "api.hanzo.ai"}
	if kc != nil {
		deps.KMS = kc
	}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return app
}

type httpResult struct {
	Code     int
	Location string
	Body     []byte
}

// req issues one request. org != "" sets the gateway identity headers (X-Org-Id +
// a validated X-User-Id) exactly as SanitizeIdentity would; org == "" sends NO
// identity — the public / anonymous-forge path the callback and 403 tests need.
func req(t *testing.T, app *zip.App, method, path, org string, body any) httpResult {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	rq := httptest.NewRequest(method, path, r)
	if body != nil {
		rq.Header.Set("Content-Type", "application/json")
	}
	if org != "" {
		rq.Header.Set("X-Org-Id", org)
		rq.Header.Set("X-User-Id", "u-"+org)
	}
	resp, err := app.Fiber().Test(rq)
	if err != nil {
		t.Fatalf("Test %s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return httpResult{Code: resp.StatusCode, Location: resp.Header.Get("Location"), Body: b}
}

// connectSlack runs the connect → callback handshake for org and returns the
// callback result. It extracts the real signed state from the authorize URL, so
// the callback is exercised with a genuine state (not a hand-forged one).
func connectSlack(t *testing.T, app *zip.App, org, code string) httpResult {
	t.Helper()
	res := req(t, app, http.MethodPost, "/v1/integrations/slack/connect", org, nil)
	if res.Code != http.StatusOK {
		t.Fatalf("connect want 200, got %d (%s)", res.Code, res.Body)
	}
	var out struct {
		AuthorizeURL string `json:"authorizeUrl"`
	}
	if err := json.Unmarshal(res.Body, &out); err != nil {
		t.Fatalf("connect body: %v (%s)", err, res.Body)
	}
	u, err := url.Parse(out.AuthorizeURL)
	if err != nil {
		t.Fatalf("authorizeUrl parse: %v", err)
	}
	state := u.Query().Get("state")
	if state == "" {
		t.Fatal("authorizeUrl missing state")
	}
	return req(t, app, http.MethodGet,
		"/v1/integrations/slack/callback?code="+url.QueryEscape(code)+"&state="+url.QueryEscape(state), "", nil)
}

func slackConfiguredEnv(t *testing.T) {
	t.Setenv(slackClientIDEnv, "test-client-id")
	t.Setenv(slackClientSecretEnv, "test-client-secret")
}

func TestIntegrationsListShape(t *testing.T) {
	slackConfiguredEnv(t)
	app := newApp(t, newKMS(t))

	// No principal → 403 (status is per-org, an org is required).
	if r := req(t, app, http.MethodGet, "/v1/integrations", "", nil); r.Code != http.StatusForbidden {
		t.Fatalf("no-principal list want 403, got %d", r.Code)
	}

	r := req(t, app, http.MethodGet, "/v1/integrations", "acme", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("list want 200, got %d (%s)", r.Code, r.Body)
	}
	var out struct {
		Providers []providerView `json:"providers"`
	}
	if err := json.Unmarshal(r.Body, &out); err != nil {
		t.Fatalf("list body: %v (%s)", err, r.Body)
	}
	byID := map[string]providerView{}
	for _, p := range out.Providers {
		byID[p.ID] = p
	}
	slack, ok := byID["slack"]
	if !ok {
		t.Fatalf("slack missing from list: %+v", out.Providers)
	}
	if !slack.Available || slack.Connected {
		t.Fatalf("slack want available+not-connected, got %+v", slack)
	}
	gh, ok := byID["github"]
	if !ok {
		t.Fatal("github (scaffold) missing from list")
	}
	if gh.Available {
		t.Fatal("github must be available=false (not configured) — honest scaffold, not a dead-end")
	}

	// Unknown provider id → 404.
	if r := req(t, app, http.MethodGet, "/v1/integrations/nope", "acme", nil); r.Code != http.StatusNotFound {
		t.Fatalf("unknown provider want 404, got %d", r.Code)
	}
}

func TestIntegrationsConnectUnconfigured503(t *testing.T) {
	// github has no creds → connect is an honest 503, never a dead-end.
	app := newApp(t, newKMS(t))
	if r := req(t, app, http.MethodPost, "/v1/integrations/github/connect", "acme", nil); r.Code != http.StatusServiceUnavailable {
		t.Fatalf("connect unconfigured want 503, got %d (%s)", r.Code, r.Body)
	}
}

func TestIntegrationsConnectNoPrincipal403(t *testing.T) {
	slackConfiguredEnv(t)
	app := newApp(t, newKMS(t))
	if r := req(t, app, http.MethodPost, "/v1/integrations/slack/connect", "", nil); r.Code != http.StatusForbidden {
		t.Fatalf("connect no-principal want 403, got %d", r.Code)
	}
}

func TestIntegrationsConnectKMSNotReady503(t *testing.T) {
	slackConfiguredEnv(t)
	// Mount with NO KMS client → secret custody is impossible → connect 503,
	// never a plaintext fallback.
	app := newApp(t, nil)
	if r := req(t, app, http.MethodPost, "/v1/integrations/slack/connect", "acme", nil); r.Code != http.StatusServiceUnavailable {
		t.Fatalf("connect with KMS down want 503, got %d (%s)", r.Code, r.Body)
	}
}

func TestIntegrationsConnectInvalidOrg400(t *testing.T) {
	slackConfiguredEnv(t)
	app := newApp(t, newKMS(t))
	// A validated principal whose org would smuggle path structure into the KMS
	// key is refused at the boundary (400), before any secret op.
	if r := req(t, app, http.MethodPost, "/v1/integrations/slack/connect", "bad/org", nil); r.Code != http.StatusBadRequest {
		t.Fatalf("connect invalid org want 400, got %d (%s)", r.Code, r.Body)
	}
}

func TestIntegrationsCallbackHappyPath(t *testing.T) {
	slackConfiguredEnv(t)
	stubSlackAPI(t)
	kc := newKMS(t)
	app := newApp(t, kc)

	cb := connectSlack(t, app, "acme", "goodcode")
	if cb.Code != http.StatusFound {
		t.Fatalf("callback want 302, got %d (%s)", cb.Code, cb.Body)
	}
	if !strings.Contains(cb.Location, "console.hanzo.ai/integrations?connected=slack") {
		t.Fatalf("callback Location want console success, got %q", cb.Location)
	}

	// The bot token was SEALED into KMS (never plaintext, never SQLite).
	tok, err := kc.Get(kmsPath("acme", "slack"), "bot_token", kmsEnv)
	if err != nil {
		t.Fatalf("kms get bot_token: %v", err)
	}
	if string(tok) != "xoxb-real-token" {
		t.Fatalf("sealed token mismatch: %q", tok)
	}

	// The connection row (non-secret metadata) landed.
	conn, ok := ConnectionFor("acme", "slack")
	if !ok || conn.ExternalID != "T0TEAM" || conn.AccountLabel != "Acme Inc" {
		t.Fatalf("connection row: ok=%v %+v", ok, conn)
	}
	if conn.BotUserID != "U0BOT" {
		t.Fatalf("bot_user_id must be recorded (non-secret): %q", conn.BotUserID)
	}

	// Seam: external id → org.
	if org, ok := OrgForExternalID("slack", "T0TEAM"); !ok || org != "acme" {
		t.Fatalf("OrgForExternalID: org=%q ok=%v", org, ok)
	}

	// Seam: TokenFor (gated on connected) returns the sealed token.
	got, err := TokenFor(context.Background(), "acme", "slack", "bot_token")
	if err != nil || string(got) != "xoxb-real-token" {
		t.Fatalf("TokenFor: %q err=%v", got, err)
	}

	// The card now shows connected + the account label.
	r := req(t, app, http.MethodGet, "/v1/integrations", "acme", nil)
	var out struct {
		Providers []providerView `json:"providers"`
	}
	_ = json.Unmarshal(r.Body, &out)
	var seen bool
	for _, p := range out.Providers {
		if p.ID == "slack" {
			seen = true
			if !p.Connected || p.Connection == nil || p.Connection.Account != "Acme Inc" {
				t.Fatalf("slack card must show connected+account: %+v", p)
			}
		}
	}
	if !seen {
		t.Fatal("slack missing from list after connect")
	}
}

func TestIntegrationsCallbackReplayRejected(t *testing.T) {
	slackConfiguredEnv(t)
	stubSlackAPI(t)
	app := newApp(t, newKMS(t))

	res := req(t, app, http.MethodPost, "/v1/integrations/slack/connect", "acme", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("connect: %d", res.Code)
	}
	var out struct {
		AuthorizeURL string `json:"authorizeUrl"`
	}
	_ = json.Unmarshal(res.Body, &out)
	u, _ := url.Parse(out.AuthorizeURL)
	state := u.Query().Get("state")
	cbPath := "/v1/integrations/slack/callback?code=goodcode&state=" + url.QueryEscape(state)

	// First callback with a valid, single-use state succeeds.
	if r := req(t, app, http.MethodGet, cbPath, "", nil); r.Code != http.StatusFound || !strings.Contains(r.Location, "connected=slack") {
		t.Fatalf("first callback want success 302, got %d %q", r.Code, r.Location)
	}
	// Replaying the SAME state → nonce already consumed → failure redirect, no
	// second exchange.
	r := req(t, app, http.MethodGet, cbPath, "", nil)
	if r.Code != http.StatusFound || !strings.Contains(r.Location, "error=slack") {
		t.Fatalf("replayed callback want failure 302, got %d %q", r.Code, r.Location)
	}
}

func TestIntegrationsCallbackTamperedStateRejected(t *testing.T) {
	slackConfiguredEnv(t)
	stubSlackAPI(t)
	app := newApp(t, newKMS(t))
	// A forged state never verifies → failure redirect, NEVER an exchange.
	r := req(t, app, http.MethodGet, "/v1/integrations/slack/callback?code=goodcode&state=forged.deadbeef", "", nil)
	if r.Code != http.StatusFound || !strings.Contains(r.Location, "error=slack") {
		t.Fatalf("tampered state want failure 302, got %d %q", r.Code, r.Location)
	}
	// And nothing was stored for the org the attacker might have hoped to hit.
	if _, ok := ConnectionFor("acme", "slack"); ok {
		t.Fatal("a forged callback must never create a connection")
	}
}

func TestIntegrationsDisconnect(t *testing.T) {
	slackConfiguredEnv(t)
	stubSlackAPI(t)
	kc := newKMS(t)
	app := newApp(t, kc)

	if cb := connectSlack(t, app, "acme", "goodcode"); cb.Code != http.StatusFound {
		t.Fatalf("setup connect: %d", cb.Code)
	}
	if _, err := kc.Get(kmsPath("acme", "slack"), "bot_token", kmsEnv); err != nil {
		t.Fatalf("precondition: token must exist: %v", err)
	}

	r := req(t, app, http.MethodPost, "/v1/integrations/slack/disconnect", "acme", nil)
	if r.Code != http.StatusOK {
		t.Fatalf("disconnect want 200, got %d (%s)", r.Code, r.Body)
	}
	var out struct {
		Disconnected bool `json:"disconnected"`
	}
	_ = json.Unmarshal(r.Body, &out)
	if !out.Disconnected {
		t.Fatalf("disconnect body: %s", r.Body)
	}

	// KMS secret deleted.
	if _, err := kc.Get(kmsPath("acme", "slack"), "bot_token", kmsEnv); err == nil {
		t.Fatal("bot_token must be deleted from KMS after disconnect")
	}
	// Connection row gone.
	if _, ok := ConnectionFor("acme", "slack"); ok {
		t.Fatal("connection row must be gone after disconnect")
	}
	// Idempotent.
	if r := req(t, app, http.MethodPost, "/v1/integrations/slack/disconnect", "acme", nil); r.Code != http.StatusOK {
		t.Fatalf("second disconnect want 200, got %d", r.Code)
	}
}

func TestIntegrationsOrgIsolation(t *testing.T) {
	slackConfiguredEnv(t)
	stubSlackAPI(t)
	kc := newKMS(t)
	app := newApp(t, kc)

	if cb := connectSlack(t, app, "acme", "goodcode"); cb.Code != http.StatusFound {
		t.Fatalf("acme connect: %d", cb.Code)
	}

	// globex sees slack NOT connected.
	r := req(t, app, http.MethodGet, "/v1/integrations", "globex", nil)
	var out struct {
		Providers []providerView `json:"providers"`
	}
	_ = json.Unmarshal(r.Body, &out)
	for _, p := range out.Providers {
		if p.ID == "slack" && p.Connected {
			t.Fatal("globex must NOT see acme's slack connection")
		}
	}
	// globex TokenFor is refused (not connected) — no cross-tenant token read.
	if _, err := TokenFor(context.Background(), "globex", "slack", "bot_token"); err == nil {
		t.Fatal("globex TokenFor must fail closed (not connected)")
	}
	// acme's own token is readable.
	if tok, err := TokenFor(context.Background(), "acme", "slack", "bot_token"); err != nil || string(tok) != "xoxb-real-token" {
		t.Fatalf("acme TokenFor: %q err=%v", tok, err)
	}
	// External id resolves to the owning org only.
	if org, _ := OrgForExternalID("slack", "T0TEAM"); org != "acme" {
		t.Fatalf("external id must resolve to acme, got %q", org)
	}
}

// stubSlackTeam is stubSlackAPI with an attacker-controlled team name + bot user,
// used to prove ingest sanitization of provider-supplied metadata.
func stubSlackTeam(t *testing.T, teamName, botUser string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth.v2.access", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":           true,
			"access_token": "xoxb-real-token",
			"scope":        "chat:write",
			"bot_user_id":  botUser,
			"team":         map[string]string{"id": "T0TEAM", "name": teamName},
		})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	orig := slackWebAPIBase
	slackWebAPIBase = ts.URL
	t.Cleanup(func() { slackWebAPIBase = orig })
}

// TestConsoleRedirectNoOpenRedirect proves the callback Location is pinned to the
// env-fixed console origin: a hostile provider-supplied detail (workspace name)
// can never break out of the query into the host/scheme/path, nor inject CRLF.
func TestConsoleRedirectNoOpenRedirect(t *testing.T) {
	const base = "https://console.hanzo.ai"
	hostile := []string{
		"//evil.com",
		"https://evil.com/pwn",
		"@evil.com",
		"foo\r\nSet-Cookie: x=1",
		"a/../../etc/passwd",
		"?injected=1#frag",
		"\x00\x1b]0;title\x07",
	}
	for _, detail := range hostile {
		loc := consoleRedirectURL(base, "connected", "slack", "account", detail)
		if !strings.HasPrefix(loc, base+"/integrations?") {
			t.Fatalf("Location must stay on the console origin, got %q", loc)
		}
		if strings.ContainsAny(loc, "\r\n") {
			t.Fatalf("Location must never carry a raw CR/LF (header injection): %q", loc)
		}
		u, err := url.Parse(loc)
		if err != nil {
			t.Fatalf("Location must parse: %q: %v", loc, err)
		}
		if u.Scheme != "https" || u.Host != "console.hanzo.ai" || u.Path != "/integrations" {
			t.Fatalf("hostile detail %q broke out of the query: scheme=%q host=%q path=%q", detail, u.Scheme, u.Host, u.Path)
		}
	}
}

// TestIntegrationsCallbackSanitizesMeta proves vector 7: a crafted Slack workspace
// name (control chars + oversized) is stripped/bounded at ingest before it is
// stored, returned in JSON, logged, or reflected in the redirect Location.
func TestIntegrationsCallbackSanitizesMeta(t *testing.T) {
	slackConfiguredEnv(t)
	evilName := "Ac\r\nme\x00Inc\x1b]0;x\x07" + strings.Repeat("Z", 4096)
	stubSlackTeam(t, evilName, "U\r\nBOT")
	app := newApp(t, newKMS(t))

	cb := connectSlack(t, app, "acme", "goodcode")
	if cb.Code != http.StatusFound {
		t.Fatalf("callback want 302, got %d (%s)", cb.Code, cb.Body)
	}
	// The redirect Location carries the (escaped) account but no raw control bytes
	// and stays on the console host.
	if strings.ContainsAny(cb.Location, "\r\n\x00") {
		t.Fatalf("redirect Location must carry no raw control bytes: %q", cb.Location)
	}
	if !strings.Contains(cb.Location, "console.hanzo.ai/integrations?connected=slack") {
		t.Fatalf("redirect Location off console origin: %q", cb.Location)
	}

	conn, ok := ConnectionFor("acme", "slack")
	if !ok {
		t.Fatal("connection row must exist")
	}
	for _, s := range []string{conn.AccountLabel, conn.BotUserID} {
		for _, r := range s {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("stored metadata must contain no control chars, found %q in %q", r, s)
			}
		}
	}
	if len(conn.AccountLabel) > maxMetaLen {
		t.Fatalf("stored account label must be bounded to %d, got %d", maxMetaLen, len(conn.AccountLabel))
	}
	// The token itself was NOT mangled by sanitization (secret path is separate).
	got, err := TokenFor(context.Background(), "acme", "slack", "bot_token")
	if err != nil || string(got) != "xoxb-real-token" {
		t.Fatalf("secret token must be untouched by metadata sanitization: %q err=%v", got, err)
	}
}

// TestIntegrationsDisconnectNoPrincipal403 proves the secret-DELETE path is closed
// to the anonymous-forge (X-Org-Id, no bearer): a forged disconnect is 403 and
// NEVER deletes the victim org's connection or its custodied KMS secret.
func TestIntegrationsDisconnectNoPrincipal403(t *testing.T) {
	slackConfiguredEnv(t)
	stubSlackAPI(t)
	kc := newKMS(t)
	app := newApp(t, kc)

	if cb := connectSlack(t, app, "acme", "goodcode"); cb.Code != http.StatusFound {
		t.Fatalf("setup connect: %d", cb.Code)
	}
	// Forge: X-Org-Id present but NO validated principal (org == "" sends no headers;
	// send the header directly to simulate the bearer-less restore path).
	rq := httptest.NewRequest(http.MethodPost, "/v1/integrations/slack/disconnect", nil)
	rq.Header.Set("X-Org-Id", "acme") // forged org, no X-User-Id
	resp, err := app.Fiber().Test(rq)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("forged disconnect want 403, got %d", resp.StatusCode)
	}
	// The victim's connection + secret survive.
	if _, ok := ConnectionFor("acme", "slack"); !ok {
		t.Fatal("forged disconnect must NOT delete the connection row")
	}
	if _, err := kc.Get(kmsPath("acme", "slack"), "bot_token", kmsEnv); err != nil {
		t.Fatalf("forged disconnect must NOT delete the KMS secret: %v", err)
	}
}

// TestIntegrationsGithubScaffoldCallbackFailsClosed proves vector 9: even with a
// GENUINE signed state + live nonce for the unconfigured github scaffold, the
// callback fails closed at the Configured gate BEFORE any exchange — no connection
// row, no fabricated success. githubExchange is unreachable in production.
func TestIntegrationsGithubScaffoldCallbackFailsClosed(t *testing.T) {
	// github has NO creds in env → Configured()==false.
	app := newApp(t, newKMS(t))
	ctx := context.Background()

	// Mint a real, valid state + nonce for github using the mounted signer/store.
	nonce, err := genToken()
	if err != nil {
		t.Fatalf("nonce: %v", err)
	}
	if err := mounted.State.store.PutNonce(ctx, nonce, "acme", "github"); err != nil {
		t.Fatalf("put nonce: %v", err)
	}
	state, err := sign(mounted, "acme", "github", nonce)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	r := req(t, app, http.MethodGet,
		"/v1/integrations/github/callback?code=inst_123&state="+url.QueryEscape(state), "", nil)
	if r.Code != http.StatusFound || !strings.Contains(r.Location, "error=github") {
		t.Fatalf("github scaffold callback must fail closed, got %d %q", r.Code, r.Location)
	}
	if _, ok := ConnectionFor("acme", "github"); ok {
		t.Fatal("github scaffold must NEVER create a connection (fabricated success)")
	}
}

// TestIntegrationsCallbackOversizedCodeRejected proves the callback bounds the
// OAuth code param: an oversized code is refused before any provider exchange.
func TestIntegrationsCallbackOversizedCodeRejected(t *testing.T) {
	slackConfiguredEnv(t)
	stubSlackAPI(t)
	app := newApp(t, newKMS(t))

	res := req(t, app, http.MethodPost, "/v1/integrations/slack/connect", "acme", nil)
	if res.Code != http.StatusOK {
		t.Fatalf("connect: %d", res.Code)
	}
	var out struct {
		AuthorizeURL string `json:"authorizeUrl"`
	}
	_ = json.Unmarshal(res.Body, &out)
	u, _ := url.Parse(out.AuthorizeURL)
	state := u.Query().Get("state")

	// A code just over the handler cap but still within fasthttp's request-URI read
	// buffer, so it reaches the handler (rather than being rejected by the
	// transport) and trips the explicit maxCodeLen guard.
	huge := strings.Repeat("x", maxCodeLen+64)
	cb := req(t, app, http.MethodGet,
		"/v1/integrations/slack/callback?code="+huge+"&state="+url.QueryEscape(state), "", nil)
	if cb.Code != http.StatusFound || !strings.Contains(cb.Location, "error=slack") {
		t.Fatalf("oversized code must fail closed, got %d %q", cb.Code, cb.Location)
	}
	if _, ok := ConnectionFor("acme", "slack"); ok {
		t.Fatal("oversized code must never create a connection")
	}
}

func TestIntegrationsSeamUnmountedFailsClosed(t *testing.T) {
	// Force the unmounted state and prove every seam fails closed.
	_ = Shutdown(context.Background())
	if _, err := TokenFor(context.Background(), "acme", "slack", "bot_token"); err == nil {
		t.Fatal("TokenFor must fail when not mounted")
	}
	if _, ok := ConnectionFor("acme", "slack"); ok {
		t.Fatal("ConnectionFor must be false when not mounted")
	}
	if _, ok := OrgForExternalID("slack", "x"); ok {
		t.Fatal("OrgForExternalID must be false when not mounted")
	}
}
