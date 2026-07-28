package integrations

import (
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// bridge_adapters_test.go proves the per-platform INBOUND-AUTH (deliberately NOT
// all HMAC: Discord Ed25519 / Telegram secret-token + Login-Widget HMAC / Teams Bot
// Framework RS256-JWT), the pure parse edges, and the per-org ISOLATION gate
// (OrgForExternalID) shared by every adapter on the ChatBridge core.

// ── Discord: Ed25519 interaction signature ──────────────────────────────────

func TestDiscordSignatureVerify(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pubHex := hex.EncodeToString(pub)
	ts := "1700000000"
	body := []byte(`{"type":1}`)
	sig := ed25519.Sign(priv, append([]byte(ts), body...))
	sigHex := hex.EncodeToString(sig)

	if !verifyDiscordSignature(pubHex, sigHex, ts, body) {
		t.Fatal("valid Ed25519 signature must verify")
	}
	// Tampered body → fail.
	if verifyDiscordSignature(pubHex, sigHex, ts, []byte(`{"type":2}`)) {
		t.Fatal("tampered body must NOT verify")
	}
	// Tampered timestamp → fail (timestamp is part of the signed message).
	if verifyDiscordSignature(pubHex, sigHex, "1700000001", body) {
		t.Fatal("tampered timestamp must NOT verify")
	}
	// Wrong key → fail.
	otherPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if verifyDiscordSignature(hex.EncodeToString(otherPub), sigHex, ts, body) {
		t.Fatal("wrong public key must NOT verify")
	}
	// Malformed inputs never panic, always false.
	if verifyDiscordSignature("zz", sigHex, ts, body) || verifyDiscordSignature(pubHex, "zz", ts, body) ||
		verifyDiscordSignature("", sigHex, ts, body) || verifyDiscordSignature(pubHex, "", ts, body) {
		t.Fatal("malformed inputs must be rejected")
	}
}

func TestParseDiscordInteraction(t *testing.T) {
	if it, ok := parseDiscordInteraction([]byte(`{"type":1,"id":"i1"}`)); !ok || it.Type != discordTypePing {
		t.Fatalf("PING parse: ok=%v type=%d", ok, it.Type)
	}
	cmd := `{"type":2,"id":"i2","application_id":"app","token":"tok","guild_id":"G1","channel_id":"C1",
	         "member":{"user":{"id":"U1"}},"data":{"name":"hanzo","options":[{"name":"prompt","value":"hello there"}]}}`
	it, ok := parseDiscordInteraction([]byte(cmd))
	if !ok {
		t.Fatal("command parse failed")
	}
	if it.GuildID != "G1" || it.User != "U1" || it.CommandName != "hanzo" || it.Prompt != "hello there" || it.Token != "tok" {
		t.Fatalf("bad parse: %+v", it)
	}
	// DM interaction (user, no member).
	dm := `{"type":2,"id":"i3","data":{"name":"hanzo","options":[{"name":"prompt","value":"hi"}]},"user":{"id":"U9"}}`
	if it, _ := parseDiscordInteraction([]byte(dm)); it.User != "U9" {
		t.Fatalf("DM user parse: %q", it.User)
	}
}

// TestDiscordInteractionsHandler proves the Ed25519 gate + PING/PONG + the
// unconnected-guild drop (isolation) at the handler.
func TestDiscordInteractionsHandler(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	t.Setenv(discordClientIDEnv, "dcid")
	t.Setenv(discordClientSecretEnv, "dcsecret")
	t.Setenv(discordPublicKeyEnv, hex.EncodeToString(pub))
	app := newApp(t, newKMS(t))

	post := func(body string, sign bool) httpResult {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		rq := httptest.NewRequest(http.MethodPost, "/v1/integrations/discord/interactions", strings.NewReader(body))
		rq.Header.Set("Content-Type", "application/json")
		if sign {
			sig := ed25519.Sign(priv, append([]byte(ts), []byte(body)...))
			rq.Header.Set(discordSigHeader, hex.EncodeToString(sig))
			rq.Header.Set(discordTimestampHeader, ts)
		}
		resp, err := app.Fiber().Test(rq)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		return httpResult{Code: resp.StatusCode, Body: b}
	}

	// Unsigned / bad signature → 401.
	if r := post(`{"type":1,"id":"p1"}`, false); r.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned PING want 401, got %d", r.Code)
	}
	// Signed PING → 200 PONG (type 1).
	if r := post(`{"type":1,"id":"p2"}`, true); r.Code != http.StatusOK || !strings.Contains(string(r.Body), `"type":1`) {
		t.Fatalf("signed PING want 200 PONG, got %d %s", r.Code, r.Body)
	}
	// Command for an UNCONNECTED guild → 200 with an ephemeral "not connected"
	// message (isolation: no run dispatched for a guild no org connected).
	cmd := `{"type":2,"id":"c1","application_id":"app","token":"tok","guild_id":"GNOPE","member":{"user":{"id":"U1"}},"data":{"name":"hanzo","options":[{"name":"prompt","value":"hi"}]}}`
	if r := post(cmd, true); r.Code != http.StatusOK || !strings.Contains(string(r.Body), "isn't connected") {
		t.Fatalf("unconnected-guild command want ephemeral not-connected, got %d %s", r.Code, r.Body)
	}
}

// ── Telegram: secret-token + Login Widget ───────────────────────────────────

func TestVerifyTelegramLogin(t *testing.T) {
	const bot = "123:abcBOTTOKEN"
	now := time.Now().Unix()
	q := map[string]string{
		"id": "42", "first_name": "Ada", "username": "ada", "auth_date": strconv.FormatInt(now, 10),
	}
	// Compute the correct widget hash.
	q["hash"] = telegramLoginHash(bot, q)
	if id, ok := verifyTelegramLogin(bot, q, now); !ok || id != "42" {
		t.Fatalf("valid widget auth must verify to id 42, got id=%q ok=%v", id, ok)
	}
	// Tampered field → fail.
	bad := map[string]string{"id": "99", "first_name": "Ada", "username": "ada", "auth_date": q["auth_date"], "hash": q["hash"]}
	if _, ok := verifyTelegramLogin(bot, bad, now); ok {
		t.Fatal("tampered id must NOT verify")
	}
	// Wrong bot token → fail.
	if _, ok := verifyTelegramLogin("999:other", q, now); ok {
		t.Fatal("wrong bot token must NOT verify")
	}
	// Stale auth_date → fail.
	if _, ok := verifyTelegramLogin(bot, q, now+telegramLoginMaxAgeSec+10); ok {
		t.Fatal("stale auth_date must NOT verify")
	}
}

// telegramLoginHash reproduces Telegram's widget signature for tests.
func telegramLoginHash(botToken string, q map[string]string) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		if k != "hash" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, k := range keys {
		lines = append(lines, k+"="+q[k])
	}
	secret := sha256.Sum256([]byte(botToken))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestParseTelegramUpdate(t *testing.T) {
	raw := `{"update_id":10,"message":{"message_id":5,"text":"@HanzoAIBot hi","from":{"id":7,"is_bot":false},"chat":{"id":-100,"type":"supergroup","title":"T"}}}`
	m, ok := parseTelegramUpdate([]byte(raw))
	if !ok || m.UpdateID != 10 || m.ChatID != -100 || m.UserID != 7 || m.ChatType != "supergroup" {
		t.Fatalf("bad parse: %+v ok=%v", m, ok)
	}
	// Bot's own message → dropped.
	if _, ok := parseTelegramUpdate([]byte(`{"update_id":1,"message":{"message_id":1,"text":"x","from":{"id":1,"is_bot":true},"chat":{"id":1,"type":"private"}}}`)); ok {
		t.Fatal("bot message must be dropped")
	}
	// Non-message update → dropped.
	if _, ok := parseTelegramUpdate([]byte(`{"update_id":2,"edited_message":{}}`)); ok {
		t.Fatal("non-message update must be dropped")
	}
}

func TestTelegramTriggerAndConnectCode(t *testing.T) {
	t.Setenv(telegramBotUsernameEnv, "HanzoAIBot")
	// Private chat: everything is a trigger.
	if p, ok := telegramTrigger(telegramMessage{ChatType: "private", Text: "what's up"}); !ok || p != "what's up" {
		t.Fatalf("private trigger: %q %v", p, ok)
	}
	// Group without mention: not a trigger.
	if _, ok := telegramTrigger(telegramMessage{ChatType: "supergroup", Text: "hello team"}); ok {
		t.Fatal("group message without mention must not trigger")
	}
	// Group with @mention: trigger, mention stripped.
	if p, ok := telegramTrigger(telegramMessage{ChatType: "supergroup", Text: "@HanzoAIBot summarize this"}); !ok || p != "summarize this" {
		t.Fatalf("group mention trigger: %q %v", p, ok)
	}
	// /hanzo command anywhere.
	if p, ok := telegramTrigger(telegramMessage{ChatType: "supergroup", Text: "/hanzo@HanzoAIBot do it"}); !ok || p != "do it" {
		t.Fatalf("/hanzo command trigger: %q %v", p, ok)
	}
	// Connect code parse.
	if code, ok := telegramConnectCode("/start abc123"); !ok || code != "abc123" {
		t.Fatalf("/start code: %q %v", code, ok)
	}
	if code, ok := telegramConnectCode("/connect@HanzoAIBot xyz"); !ok || code != "xyz" {
		t.Fatalf("/connect code: %q %v", code, ok)
	}
	if _, ok := telegramConnectCode("/start"); ok {
		t.Fatal("bare /start is not a connect")
	}
}

// TestTelegramWebhookAuthAndIsolation proves the secret-token gate + the
// chat→org isolation gate end-to-end: a wrong secret is 401; a bound chat
// dispatches (a reply hits the stubbed Bot API); an UNBOUND chat is dropped (no
// reply), so a message can never reach an org that did not bind that chat.
func TestTelegramWebhookAuthAndIsolation(t *testing.T) {
	const secret = "tg-webhook-secret"
	t.Setenv(telegramBotTokenEnv, "111:BOTTOKEN")
	t.Setenv(telegramWebhookSecretEnv, secret)
	t.Setenv(telegramBotUsernameEnv, "HanzoAIBot")

	sent := make(chan url.Values, 4)
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			sent <- r.Form
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(stub.Close)
	savedAPI := telegramAPIBase
	telegramAPIBase = stub.URL
	t.Cleanup(func() { telegramAPIBase = savedAPI })

	app := newApp(t, newKMS(t))
	// Bind chat 555 to org "acme".
	if err := mounted.State.store.Upsert(context.Background(), Connection{Org: "acme", Provider: "telegram", ExternalID: "555"}); err != nil {
		t.Fatal(err)
	}

	post := func(secretHdr, body string) int {
		rq := httptest.NewRequest(http.MethodPost, "/v1/integrations/telegram/webhook", strings.NewReader(body))
		rq.Header.Set("Content-Type", "application/json")
		if secretHdr != "" {
			rq.Header.Set(telegramSecretHeader, secretHdr)
		}
		resp, err := app.Fiber().Test(rq)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}

	// Wrong secret → 401, no dispatch.
	if code := post("nope", `{"update_id":1,"message":{"message_id":1,"text":"hi","from":{"id":9},"chat":{"id":555,"type":"private"}}}`); code != http.StatusUnauthorized {
		t.Fatalf("wrong secret want 401, got %d", code)
	}
	// Correct secret, UNBOUND chat 777 → 200, NO reply (isolation: dropped).
	if code := post(secret, `{"update_id":2,"message":{"message_id":1,"text":"hi","from":{"id":9},"chat":{"id":777,"type":"private"}}}`); code != http.StatusOK {
		t.Fatalf("unbound chat want 200, got %d", code)
	}
	select {
	case s := <-sent:
		t.Fatalf("an unbound chat must produce NO reply, saw %v", s)
	case <-time.After(400 * time.Millisecond):
	}
	// Correct secret, BOUND chat 555, unlinked user → 200 + a link-prompt reply to
	// chat 555 (the brain runs on-behalf, finds no link, returns the connect prompt).
	if code := post(secret, `{"update_id":3,"message":{"message_id":8,"text":"hello","from":{"id":9},"chat":{"id":555,"type":"private"}}}`); code != http.StatusOK {
		t.Fatalf("bound chat want 200, got %d", code)
	}
	select {
	case s := <-sent:
		if s.Get("chat_id") != "555" {
			t.Fatalf("reply must go to the BOUND chat 555, got chat_id=%q", s.Get("chat_id"))
		}
		if !strings.Contains(s.Get("text"), "Connect your Hanzo account") {
			t.Fatalf("unlinked user must get the connect prompt, got %q", s.Get("text"))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bound chat must produce a reply")
	}
}

// ── Teams: Bot Framework RS256 JWT (JWKS) ───────────────────────────────────

func TestVerifyBotFrameworkJWT(t *testing.T) {
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const kid = "test-kid-1"
	const appID = "app-guid"
	const svcURL = "https://smba.trafficmanager.net/amer/"

	// Stub Bot Framework's OpenID config + JWKS.
	mux := http.NewServeMux()
	mux.HandleFunc("/openid", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"jwks_uri": "http://" + r.Host + "/keys"})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
		n := base64.RawURLEncoding.EncodeToString(priv.PublicKey.N.Bytes())
		e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(priv.PublicKey.E)).Bytes())
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{
			{"kty": "RSA", "kid": kid, "n": n, "e": e},
		}})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	savedOpenID := teamsOpenIDURL
	teamsOpenIDURL = func() string { return srv.URL + "/openid" }
	t.Cleanup(func() { teamsOpenIDURL = savedOpenID })
	// Reset the package JWKS cache so the test's keys are fetched.
	teamsKeysMu.Lock()
	teamsKeys = nil
	teamsKeysAt = time.Time{}
	teamsKeysMu.Unlock()

	now := time.Now().Unix()
	valid := signTestRS256(t, priv, kid, map[string]any{
		"iss": botFrameworkIssuer, "aud": appID, "exp": now + 3600, "nbf": now - 60, "serviceurl": svcURL,
	})
	if !verifyBotFrameworkJWT(context.Background(), "Bearer "+valid, appID, svcURL, now) {
		t.Fatal("valid Bot Framework JWT must verify")
	}
	// Wrong audience → fail.
	if verifyBotFrameworkJWT(context.Background(), "Bearer "+valid, "other-app", svcURL, now) {
		t.Fatal("wrong audience must NOT verify")
	}
	// serviceurl mismatch → fail (reply-redirection defense).
	if verifyBotFrameworkJWT(context.Background(), "Bearer "+valid, appID, "https://evil.example/", now) {
		t.Fatal("serviceurl mismatch must NOT verify")
	}
	// Wrong issuer → fail.
	wrongIss := signTestRS256(t, priv, kid, map[string]any{"iss": "https://evil", "aud": appID, "exp": now + 3600, "serviceurl": svcURL})
	if verifyBotFrameworkJWT(context.Background(), "Bearer "+wrongIss, appID, svcURL, now) {
		t.Fatal("wrong issuer must NOT verify")
	}
	// Expired → fail.
	expired := signTestRS256(t, priv, kid, map[string]any{"iss": botFrameworkIssuer, "aud": appID, "exp": now - 10000, "serviceurl": svcURL})
	if verifyBotFrameworkJWT(context.Background(), "Bearer "+expired, appID, svcURL, now) {
		t.Fatal("expired token must NOT verify")
	}
	// Algorithm-confusion: alg=none must be rejected before any key work.
	noneTok := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","kid":"`+kid+`"}`)) + "." +
		base64.RawURLEncoding.EncodeToString([]byte(`{"iss":"`+botFrameworkIssuer+`","aud":"`+appID+`","serviceurl":"`+svcURL+`"}`)) + "."
	if verifyBotFrameworkJWT(context.Background(), "Bearer "+noneTok, appID, svcURL, now) {
		t.Fatal("alg=none must NOT verify")
	}
	// Unknown kid → fail.
	unknown := signTestRS256(t, priv, "no-such-kid", map[string]any{"iss": botFrameworkIssuer, "aud": appID, "exp": now + 3600, "serviceurl": svcURL})
	if verifyBotFrameworkJWT(context.Background(), "Bearer "+unknown, appID, svcURL, now) {
		t.Fatal("unknown kid must NOT verify")
	}
}

// signTestRS256 mints an RS256 JWT for the test key.
func signTestRS256(t *testing.T, priv *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": kid, "typ": "JWT"})
	pl, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString(hdr) + "." + base64.RawURLEncoding.EncodeToString(pl)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, priv, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestParseTeamsActivityAndStripMentions(t *testing.T) {
	raw := `{"type":"message","id":"a1","text":"<at>Hanzo</at> summarize the thread","serviceUrl":"https://smba/","from":{"id":"29:u","aadObjectId":"oid-1"},"conversation":{"id":"19:conv@thread.tacv2"},"channelData":{"tenant":{"id":"tid-1"}}}`
	act, ok := parseTeamsActivity([]byte(raw))
	if !ok || act.TenantID != "tid-1" || act.UserAADObjectID != "oid-1" || act.ConversationID != "19:conv@thread.tacv2" || act.ServiceURL != "https://smba/" {
		t.Fatalf("bad parse: %+v ok=%v", act, ok)
	}
	if got := stripTeamsMentions(act.Text); got != "summarize the thread" {
		t.Fatalf("mention strip: %q", got)
	}
	if got := stripTeamsMentions("<at>Bot</at> hi <at>Other</at> there"); got != "hi  there" {
		t.Fatalf("multi mention strip: %q", got)
	}
	// No serviceUrl → not parseable (nothing to reply to).
	if _, ok := parseTeamsActivity([]byte(`{"type":"message","id":"a2","text":"hi"}`)); ok {
		t.Fatal("activity without serviceUrl must not parse")
	}
}

// ── per-org isolation (the OrgForExternalID gate, every provider) ────────────

// TestBridgeOrgIsolationAllProviders proves the store's ResolveOrgByExternalID —
// the isolation root shared by every adapter — is EXACT: an external id resolves to
// ONLY the org that connected it, a foreign id resolves to nothing, and two orgs on
// two ids never cross.
func TestBridgeOrgIsolationAllProviders(t *testing.T) {
	st, err := openStore(t.TempDir() + "/iso.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	cases := []struct{ provider, extA, extB string }{
		{"discord", "guildA", "guildB"},
		{"teams", "tenantA", "tenantB"},
		{"telegram", "555", "777"},
	}
	for _, c := range cases {
		if err := st.Upsert(ctx, Connection{Org: "orgA", Provider: c.provider, ExternalID: c.extA}); err != nil {
			t.Fatal(err)
		}
		if err := st.Upsert(ctx, Connection{Org: "orgB", Provider: c.provider, ExternalID: c.extB}); err != nil {
			t.Fatal(err)
		}
		if org, ok, _ := st.ResolveOrgByExternalID(ctx, c.provider, c.extA); !ok || org != "orgA" {
			t.Fatalf("%s: extA must resolve orgA, got %q %v", c.provider, org, ok)
		}
		if org, ok, _ := st.ResolveOrgByExternalID(ctx, c.provider, c.extB); !ok || org != "orgB" {
			t.Fatalf("%s: extB must resolve orgB, got %q %v", c.provider, org, ok)
		}
		// A foreign / never-connected id resolves to NOTHING (dropped, never leaked).
		if _, ok, _ := st.ResolveOrgByExternalID(ctx, c.provider, "ghost"); ok {
			t.Fatalf("%s: unconnected id must resolve to nothing", c.provider)
		}
		// Cross-provider: orgA's discord id is not a telegram/teams id.
		if _, ok, _ := st.ResolveOrgByExternalID(ctx, "telegram", "guildA"); ok {
			t.Fatalf("%s id must not resolve under a different provider", c.provider)
		}
	}
}

// TestProvidersRegistered asserts the 4 chat platforms are on the registry (so
// their console cards auto-appear, available gated on Configured).
func TestProvidersRegistered(t *testing.T) {
	for _, id := range []string{"slack", "discord", "teams", "telegram"} {
		p, ok := registry[id]
		if !ok {
			t.Fatalf("provider %q not registered", id)
		}
		if p.Category != "Communication" {
			t.Fatalf("provider %q category = %q, want Communication", id, p.Category)
		}
		if p.RedirectPath != callbackPath(id) {
			t.Fatalf("provider %q RedirectPath %q != %q", id, p.RedirectPath, callbackPath(id))
		}
	}
}
