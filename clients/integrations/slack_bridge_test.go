package integrations

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// The bridge routes are registered by the real integrations.Mount (via newApp),
// so tests drive them directly — no test-only route wiring.

// slackSign computes Slack's v0 request signature over (timestamp, rawBody).
func slackSign(secret, ts, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + ts + ":" + body))
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

// slackPost issues a signed Slack webhook/slash request with a fresh timestamp.
func slackPost(t *testing.T, app *zip.App, path, secret, contentType, body string) httpResult {
	t.Helper()
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	rq := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rq.Header.Set("X-Slack-Request-Timestamp", ts)
	rq.Header.Set("X-Slack-Signature", slackSign(secret, ts, body))
	rq.Header.Set("Content-Type", contentType)
	resp, err := app.Fiber().Test(rq)
	if err != nil {
		t.Fatalf("Test %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return httpResult{Code: resp.StatusCode, Location: resp.Header.Get("Location"), Body: b}
}

// stubSlackBridge repoints the Slack Web API at an httptest server that (a) maps a
// connect/user OAuth code to a team + bot/user identity, and (b) captures the
// Authorization bearer of every chat.* post onto authCh — so a test can prove
// WHICH org's bot token replied. Restores slackWebAPIBase after the test.
func stubSlackBridge(t *testing.T, authCh chan<- string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth.v2.access", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		w.Header().Set("Content-Type", "application/json")
		var team, token, botUser, authedUser string
		switch r.FormValue("code") {
		case "acmecode":
			team, token, botUser = "TACME", "xoxb-acme", "BACME"
		case "globexcode":
			team, token, botUser = "TGLOBEX", "xoxb-globex", "BGLOBEX"
		case "usercode-acme":
			team, authedUser = "TACME", "Uacme"
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_code"})
			return
		}
		resp := map[string]any{
			"ok":           true,
			"access_token": token,
			"scope":        "chat:write",
			"bot_user_id":  botUser,
			"team":         map[string]string{"id": team, "name": team},
		}
		if authedUser != "" {
			resp["authed_user"] = map[string]string{"id": authedUser}
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	chat := func(w http.ResponseWriter, r *http.Request) {
		select {
		case authCh <- r.Header.Get("Authorization"):
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
	mux.HandleFunc("/chat.postEphemeral", chat)
	mux.HandleFunc("/chat.postMessage", chat)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	orig := slackWebAPIBase
	slackWebAPIBase = ts.URL
	t.Cleanup(func() { slackWebAPIBase = orig })
}

// ── crypto primitives (pure) ────────────────────────────────────────────────

func TestSlackSignatureVerify(t *testing.T) {
	const secret = "8f742231b10e8888abcd99yyyzzz85a5"
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	body := `{"type":"url_verification","challenge":"x"}`
	good := slackSign(secret, ts, body)

	if !verifySlackSignature(secret, good, ts, body, 0) {
		t.Fatal("a correct signature must verify")
	}
	// Tampered body → fail.
	if verifySlackSignature(secret, good, ts, body+"tamper", 0) {
		t.Fatal("a body-tampered request must fail")
	}
	// Wrong secret → fail.
	if verifySlackSignature("other", good, ts, body, 0) {
		t.Fatal("a wrong signing secret must fail")
	}
	// Stale timestamp (outside the 5-min window) → fail even with a valid MAC.
	oldTs := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	if verifySlackSignature(secret, slackSign(secret, oldTs, body), oldTs, body, 0) {
		t.Fatal("a stale timestamp must fail (anti-replay window)")
	}
	// Empty inputs → fail (never panic).
	if verifySlackSignature("", "", "", "", 0) {
		t.Fatal("empty inputs must fail")
	}
}

func TestSlackSubjectStateRoundtrip(t *testing.T) {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 7)
	}
	st, err := signSlackSubject(key, "nonce-abc", 0)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sub, nonce, ok := verifySlackSubject(key, st, 0)
	if !ok || sub != "nonce-abc" || nonce == "" {
		t.Fatalf("roundtrip mismatch: sub=%q nonce=%q ok=%v", sub, nonce, ok)
	}
	// Tampered MAC → fail.
	if _, _, ok := verifySlackSubject(key, st[:len(st)-1]+"Z", 0); ok {
		t.Fatal("tampered state must fail")
	}
	// Different key → fail (a forged state under the wrong key never verifies).
	other := make([]byte, 32)
	if _, _, ok := verifySlackSubject(other, st, 0); ok {
		t.Fatal("state under a different key must fail")
	}
	// Expired → fail.
	past, _ := signSlackSubject(key, "s", time.Now().Add(-slackLinkTTLSec*time.Second-time.Minute).Unix())
	if _, _, ok := verifySlackSubject(key, past, 0); ok {
		t.Fatal("expired state must fail")
	}
	// Link subject (team:user) roundtrips.
	ls, _ := signSlackLink(key, "TACME", "Uacme", 0)
	team, user, _, lok := verifySlackLink(key, ls, 0)
	if !lok || team != "TACME" || user != "Uacme" {
		t.Fatalf("link state roundtrip: team=%q user=%q ok=%v", team, user, lok)
	}
}

// ── event routing (pure) ────────────────────────────────────────────────────

func TestRouteSlackEvent(t *testing.T) {
	// url_verification → challenge.
	if d := routeSlackEvent([]byte(`{"type":"url_verification","challenge":"c123"}`)); d.Kind != slackRouteChallenge || d.Challenge != "c123" {
		t.Fatalf("challenge route: %+v", d)
	}
	// app_mention → agent, mention token stripped, threads under ts.
	d := routeSlackEvent([]byte(`{"type":"event_callback","team_id":"T1","event_id":"E1","event":{"type":"app_mention","user":"U1","text":"<@BOT> hello there","channel":"C1","ts":"111.1"}}`))
	if d.Kind != slackRouteAgent || d.TeamID != "T1" || d.User != "U1" || d.Text != "hello there" || d.ThreadTS != "111.1" {
		t.Fatalf("app_mention route: %+v", d)
	}
	// DM (channel_type=im) → agent.
	d = routeSlackEvent([]byte(`{"type":"event_callback","team_id":"T1","event_id":"E2","event":{"type":"message","channel_type":"im","user":"U1","text":"hi","channel":"D1","ts":"222.2"}}`))
	if d.Kind != slackRouteAgent || d.ThreadTS != "222.2" {
		t.Fatalf("dm route: %+v", d)
	}
	// Bot's own message → ack (echo-loop guard).
	if d := routeSlackEvent([]byte(`{"type":"event_callback","team_id":"T1","event_id":"E3","event":{"type":"message","channel_type":"im","bot_id":"B1","text":"echo","channel":"D1","ts":"3.3"}}`)); d.Kind != slackRouteAck {
		t.Fatalf("bot echo must ack, got %+v", d)
	}
	// Plain channel message (no mention, not im) → ack (not an agent trigger).
	if d := routeSlackEvent([]byte(`{"type":"event_callback","team_id":"T1","event_id":"E4","event":{"type":"message","channel_type":"channel","user":"U1","text":"just chatting","channel":"C1","ts":"4.4"}}`)); d.Kind != slackRouteAck {
		t.Fatalf("plain channel message must ack, got %+v", d)
	}
	// Garbage → ignore.
	if d := routeSlackEvent([]byte(`not json`)); d.Kind != slackRouteIgnore {
		t.Fatalf("garbage must ignore, got %+v", d)
	}
	// event_id dedupe key.
	if k := slackEventKey([]byte(`{"type":"event_callback","event_id":"EVID"}`)); k != "EVID" {
		t.Fatalf("event key = %q, want EVID", k)
	}
}

// ── durable dedupe ──────────────────────────────────────────────────────────

func TestSlackDedupeIdempotency(t *testing.T) {
	slackConfiguredEnv(t)
	newApp(t, newKMS(t)) // mounts the store (table created in migrate); sets `mounted`
	ctx := context.Background()

	fresh, err := mounted.store.MarkSlackEvent(ctx, "Ev-1")
	if err != nil || !fresh {
		t.Fatalf("first sighting must be fresh (err=%v fresh=%v)", err, fresh)
	}
	again, err := mounted.store.MarkSlackEvent(ctx, "Ev-1")
	if err != nil || again {
		t.Fatalf("a Slack retry of the same event_id must be a duplicate (err=%v again=%v)", err, again)
	}
	// A different id is fresh; an empty key is non-dedupable (always fresh).
	if f, _ := mounted.store.MarkSlackEvent(ctx, "Ev-2"); !f {
		t.Fatal("a distinct event_id must be fresh")
	}
	if f, _ := mounted.store.MarkSlackEvent(ctx, ""); !f {
		t.Fatal("an empty key must be non-dedupable (fresh)")
	}
}

// ── HMAC gate on the live webhook ───────────────────────────────────────────

func TestSlackEventsHMAC(t *testing.T) {
	slackConfiguredEnv(t)
	t.Setenv("SLACK_SIGNING_SECRET", "sig-secret-1")
	app := newApp(t, newKMS(t))

	body := `{"type":"url_verification","challenge":"abc123"}`

	// A bad signature is refused 401 — no challenge echoed.
	rq := httptest.NewRequest(http.MethodPost, "/v1/integrations/slack/events", strings.NewReader(body))
	rq.Header.Set("X-Slack-Signature", "v0=deadbeef")
	rq.Header.Set("X-Slack-Request-Timestamp", strconv.FormatInt(time.Now().Unix(), 10))
	resp, _ := app.Fiber().Test(rq)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bad signature want 401, got %d", resp.StatusCode)
	}

	// A missing signature is refused 401.
	rq2 := httptest.NewRequest(http.MethodPost, "/v1/integrations/slack/events", strings.NewReader(body))
	resp2, _ := app.Fiber().Test(rq2)
	_ = resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing signature want 401, got %d", resp2.StatusCode)
	}

	// A stale-but-correctly-MAC'd request is refused (anti-replay).
	oldTs := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	rq3 := httptest.NewRequest(http.MethodPost, "/v1/integrations/slack/events", strings.NewReader(body))
	rq3.Header.Set("X-Slack-Request-Timestamp", oldTs)
	rq3.Header.Set("X-Slack-Signature", slackSign("sig-secret-1", oldTs, body))
	resp3, _ := app.Fiber().Test(rq3)
	_ = resp3.Body.Close()
	if resp3.StatusCode != http.StatusUnauthorized {
		t.Fatalf("stale timestamp want 401, got %d", resp3.StatusCode)
	}

	// A correctly-signed challenge is echoed verbatim.
	res := slackPost(t, app, "/v1/integrations/slack/events", "sig-secret-1", "application/json", body)
	if res.Code != http.StatusOK || string(res.Body) != "abc123" {
		t.Fatalf("valid challenge want 200 'abc123', got %d %q", res.Code, res.Body)
	}
}

// ── THE SHIP BAR: per-org isolation, end to end ─────────────────────────────

// TestSlackBridgeOrgIsolation proves a workspace's events reach ONLY the org that
// connected that Slack team: acme owns TACME, globex owns TGLOBEX, and an
// app_mention from TACME is answered with ACME's bot token — never globex's —
// with the org derived ONLY from OrgForExternalID(team_id), never the payload.
func TestSlackBridgeOrgIsolation(t *testing.T) {
	slackConfiguredEnv(t)
	t.Setenv("SLACK_SIGNING_SECRET", "iso-secret")
	authCh := make(chan string, 4)
	stubSlackBridge(t, authCh)
	kc := newKMS(t)
	app := newApp(t, kc)
	ctx := context.Background()

	// Two orgs connect two distinct teams.
	if cb := connectSlack(t, app, "acme", "acmecode"); cb.Code != http.StatusFound {
		t.Fatalf("acme connect: %d (%s)", cb.Code, cb.Body)
	}
	if cb := connectSlack(t, app, "globex", "globexcode"); cb.Code != http.StatusFound {
		t.Fatalf("globex connect: %d (%s)", cb.Code, cb.Body)
	}

	// Seam isolation: team → connecting org, one-to-one.
	if org, ok := OrgForExternalID("slack", "TACME"); !ok || org != "acme" {
		t.Fatalf("TACME must resolve to acme, got %q ok=%v", org, ok)
	}
	if org, ok := OrgForExternalID("slack", "TGLOBEX"); !ok || org != "globex" {
		t.Fatalf("TGLOBEX must resolve to globex, got %q ok=%v", org, ok)
	}

	// Token custody is per-org: each org reads only its OWN token.
	if tok, err := TokenFor(ctx, "acme", "slack", "bot_token"); err != nil || string(tok) != "xoxb-acme" {
		t.Fatalf("acme token = %q err=%v, want xoxb-acme", tok, err)
	}
	if tok, err := TokenFor(ctx, "globex", "slack", "bot_token"); err != nil || string(tok) != "xoxb-globex" {
		t.Fatalf("globex token = %q err=%v, want xoxb-globex", tok, err)
	}

	// END TO END: an app_mention from TACME (valid HMAC), an UNLINKED user → the
	// link prompt is posted EPHEMERALLY with ACME's bot token. If the bridge ever
	// resolved the wrong org, the captured bearer would be globex's — an isolation
	// breach — or the post would never fire.
	body := `{"type":"event_callback","team_id":"TACME","event_id":"EvIso1","event":{"type":"app_mention","user":"Uacme","text":"<@BACME> hi","channel":"C1","ts":"9.9"}}`
	if res := slackPost(t, app, "/v1/integrations/slack/events", "iso-secret", "application/json", body); res.Code != http.StatusOK {
		t.Fatalf("events ack want 200, got %d (%s)", res.Code, res.Body)
	}
	select {
	case auth := <-authCh:
		if auth != "Bearer xoxb-acme" {
			t.Fatalf("ISOLATION BREACH: reply used %q, want acme's token 'Bearer xoxb-acme'", auth)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("no reply post observed (org resolution / token fetch failed)")
	}

	// And a forged team the attacker never connected resolves to NO org — the
	// bridge drops it (no org, no token, no run).
	if _, ok := OrgForExternalID("slack", "TATTACKER"); ok {
		t.Fatal("an unconnected team must resolve to no org")
	}
}

// TestSlackBridgeUnconnectedTeamDropped proves an event for a team NO org has
// connected produces no reply (no cross-org fallback, no default org).
func TestSlackBridgeUnconnectedTeamDropped(t *testing.T) {
	slackConfiguredEnv(t)
	t.Setenv("SLACK_SIGNING_SECRET", "iso-secret-2")
	authCh := make(chan string, 2)
	stubSlackBridge(t, authCh)
	app := newApp(t, newKMS(t))

	body := `{"type":"event_callback","team_id":"TNOBODY","event_id":"EvX","event":{"type":"app_mention","user":"U9","text":"<@B> hi","channel":"C1","ts":"1.1"}}`
	if res := slackPost(t, app, "/v1/integrations/slack/events", "iso-secret-2", "application/json", body); res.Code != http.StatusOK {
		t.Fatalf("ack want 200, got %d", res.Code)
	}
	select {
	case auth := <-authCh:
		t.Fatalf("an unconnected team must NEVER post a reply, but saw %q", auth)
	case <-time.After(500 * time.Millisecond):
		// good — no post
	}
}

// ── per-user link: transplant defense ───────────────────────────────────────

func slackLinkConfiguredEnv(t *testing.T) {
	slackConfiguredEnv(t)
	t.Setenv("SLACK_LINK_IAM_CLIENT_ID", "hanzo-slack")
	t.Setenv("SLACK_LINK_IAM_CLIENT_SECRET", "iam-secret")
}

func TestSlackLinkLeg1SetsCookieAndRedirects(t *testing.T) {
	slackLinkConfiguredEnv(t)
	app := newApp(t, newKMS(t))

	entry, _ := signSlackLink(mounted.stateKey, "TACME", "Uacme", 0)
	rq := httptest.NewRequest(http.MethodGet, "/v1/integrations/slack/link?state="+url.QueryEscape(entry), nil)
	resp, err := app.Fiber().Test(rq)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("leg1 want 302, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "slack.com/oauth/v2/authorize") || !strings.Contains(loc, "user_scope=openid") {
		t.Fatalf("leg1 must redirect to the Slack sign-in, got %q", loc)
	}
	sc := resp.Header.Get("Set-Cookie")
	if !strings.Contains(sc, slackInitCookie) || !strings.Contains(sc, "HttpOnly") ||
		!strings.Contains(strings.ToLower(sc), "secure") {
		t.Fatalf("leg1 must set the __Host- init cookie HttpOnly+Secure, got %q", sc)
	}
}

// cookieValue extracts the value of the named cookie from a response's Set-Cookie
// headers (there may be several — leg2 clears one and sets another).
func cookieValue(resp *http.Response, name string) (string, bool) {
	for _, sc := range resp.Header.Values("Set-Cookie") {
		if strings.HasPrefix(sc, name+"=") {
			v := strings.SplitN(sc, ";", 2)[0]
			return strings.TrimPrefix(v, name+"="), true
		}
	}
	return "", false
}

// TestSlackLinkTransplantRejected proves the F1 defense: a leg-2 (Slack sign-in
// callback) whose init cookie does not match the state's subject is refused BEFORE
// any Slack code exchange — a transplanted code+state pasted into a victim's
// browser (which carries no matching init cookie) cannot bind an attacker's Slack
// identity into the victim's session.
func TestSlackLinkTransplantRejected(t *testing.T) {
	slackLinkConfiguredEnv(t)
	// A stub that FAILS the test if the code exchange is ever reached.
	exchanged := make(chan struct{}, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth.v2.access", func(w http.ResponseWriter, r *http.Request) {
		select {
		case exchanged <- struct{}{}:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "team": map[string]string{"id": "TACME"}, "authed_user": map[string]string{"id": "Uattacker"}})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	orig := slackWebAPIBase
	slackWebAPIBase = ts.URL
	t.Cleanup(func() { slackWebAPIBase = orig })

	app := newApp(t, newKMS(t))

	ss, _ := signSlackSubject(mounted.stateKey, "nonce-A", 0)

	// (a) No init cookie at all → refused.
	res := req(t, app, http.MethodGet, "/v1/integrations/slack/link/slack?code=c&state="+url.QueryEscape(ss), "", nil)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("transplant with NO init cookie want 400, got %d (%s)", res.Code, res.Body)
	}

	// (b) A MISMATCHED init cookie → refused.
	rq := httptest.NewRequest(http.MethodGet, "/v1/integrations/slack/link/slack?code=c&state="+url.QueryEscape(ss), nil)
	rq.Header.Set("Cookie", slackInitCookie+"=WRONG-NONCE")
	resp, _ := app.Fiber().Test(rq)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("transplant with a MISMATCHED init cookie want 400, got %d", resp.StatusCode)
	}

	// The Slack code exchange must NEVER have been reached.
	select {
	case <-exchanged:
		t.Fatal("F1 BREACH: a transplanted link reached the Slack code exchange")
	default:
	}
}

// TestSlackLinkCallbackNoCookieRejected proves leg-3 refuses when the browser
// carries no Slack-verified link cookie (a forwarded OIDC callback cannot bind).
func TestSlackLinkCallbackNoCookieRejected(t *testing.T) {
	slackLinkConfiguredEnv(t)
	app := newApp(t, newKMS(t))
	res := req(t, app, http.MethodGet, "/v1/integrations/slack/link/callback?code=c&state=s", "", nil)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("leg3 without a link cookie want 400, got %d (%s)", res.Code, res.Body)
	}
}

// TestSlackLinkLeg2Continuity proves the F1 gate is not over-strict: the LEGITIMATE
// browser (init cookie == state subject) is allowed through — it exchanges the
// Slack code, sets the __Host- link cookie bound to the Slack-verified (team,user),
// and redirects into hanzo.id OIDC. Uses the connected team TACME.
func TestSlackLinkLeg2Continuity(t *testing.T) {
	slackLinkConfiguredEnv(t)
	authCh := make(chan string, 2)
	stubSlackBridge(t, authCh)
	kc := newKMS(t)
	app := newApp(t, kc)

	// acme must own TACME so leg2's "workspace connected" check passes.
	if cb := connectSlack(t, app, "acme", "acmecode"); cb.Code != http.StatusFound {
		t.Fatalf("acme connect: %d", cb.Code)
	}

	// Leg1 would set init cookie = nonce and state subject = nonce. Reproduce that
	// pairing directly: sign the slack-signin state over a known nonce and present
	// the matching init cookie.
	const nonce = "leg2-nonce"
	ss, _ := signSlackSubject(mounted.stateKey, nonce, 0)
	rq := httptest.NewRequest(http.MethodGet, "/v1/integrations/slack/link/slack?code=usercode-acme&state="+url.QueryEscape(ss), nil)
	rq.Header.Set("Cookie", slackInitCookie+"="+nonce)
	resp, err := app.Fiber().Test(rq)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("leg2 (legit browser) want 302 to OIDC, got %d", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	if !strings.Contains(loc, "/v1/iam/oauth/authorize") {
		t.Fatalf("leg2 must redirect to hanzo.id OIDC, got %q", loc)
	}
	linkVal, ok := cookieValue(resp, slackLinkCookie)
	if !ok || linkVal == "" {
		t.Fatalf("leg2 must set the __Host- link cookie, got %v", resp.Header.Values("Set-Cookie"))
	}
	// The link cookie must carry the Slack-VERIFIED (team,user) from the exchange.
	team, user, _, vok := verifySlackLink(mounted.stateKey, linkVal, 0)
	if !vok || team != "TACME" || user != "Uacme" {
		t.Fatalf("link cookie must bind the Slack-verified (team,user), got team=%q user=%q ok=%v", team, user, vok)
	}
	// The OIDC state must equal the link cookie value (browser binding for leg3).
	u, _ := url.Parse(loc)
	if st := u.Query().Get("state"); st != linkVal {
		t.Fatalf("OIDC state must equal the link cookie value; state=%q cookie=%q", st, linkVal)
	}
}

// ── route wiring: precedence + public (Red H1) ──────────────────────────────

// TestSlackRoutePrecedence proves the literal bridge routes registered by Mount
// win over the /:provider wildcard, and that the webhook is PUBLIC at the JWT
// layer (reachable with no principal). Drives the REAL Mount (via newApp), not a
// test-only wiring.
func TestSlackRoutePrecedence(t *testing.T) {
	slackLinkConfiguredEnv(t)
	t.Setenv("SLACK_SIGNING_SECRET", "prec-secret")
	app := newApp(t, newKMS(t))

	// GET /v1/integrations/slack/link must hit slackLink (302 to Slack sign-in),
	// NOT the /:provider GET handler (which would 200 a provider JSON view / 403).
	entry, _ := signSlackLink(mounted.stateKey, "TACME", "Uacme", 0)
	rq := httptest.NewRequest(http.MethodGet, "/v1/integrations/slack/link?state="+url.QueryEscape(entry), nil)
	resp, err := app.Fiber().Test(rq)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusFound || !strings.Contains(resp.Header.Get("Location"), "slack.com/oauth/v2/authorize") {
		t.Fatalf("GET /slack/link must hit slackLink (302 to Slack), got %d loc=%q", resp.StatusCode, resp.Header.Get("Location"))
	}

	// The /:provider route still resolves for the bare provider id: GET
	// /v1/integrations/slack (org-authed) returns the provider view — the literals
	// did not shadow it.
	if r := req(t, app, http.MethodGet, "/v1/integrations/slack", "acme", nil); r.Code != http.StatusOK {
		t.Fatalf("GET /v1/integrations/slack (provider view) want 200, got %d (%s)", r.Code, r.Body)
	}

	// The events webhook is PUBLIC at the JWT layer: a valid-HMAC challenge with NO
	// principal reaches slackEvents and echoes — not blocked by any auth gate.
	body := `{"type":"url_verification","challenge":"pc"}`
	if res := slackPost(t, app, "/v1/integrations/slack/events", "prec-secret", "application/json", body); res.Code != http.StatusOK || string(res.Body) != "pc" {
		t.Fatalf("events webhook must be reachable unauthenticated, got %d %q", res.Code, res.Body)
	}
}

// TestOrgLimiter proves the per-org availability isolation (Red M1): one org can
// hold at most its per-org cap, cannot starve a different org, and the global cap
// bounds the total.
func TestOrgLimiter(t *testing.T) {
	l := newOrgLimiter(2, 1) // global 2, per-org 1

	if !l.acquire("a") {
		t.Fatal("first acquire for a must succeed")
	}
	if l.acquire("a") {
		t.Fatal("a is at its per-org cap (1) — a second acquire must fail (one tenant can't monopolize)")
	}
	if !l.acquire("b") {
		t.Fatal("a different org b must still acquire even though a is capped")
	}
	if l.acquire("c") {
		t.Fatal("global pool is full (2 = a+b) — c must fail")
	}
	l.release("a") // frees a's per-org slot AND a global slot
	if !l.acquire("a") {
		t.Fatal("after release, a must acquire again")
	}
	// clamps: perOrg is capped to global; zero/negative caps floor to 1.
	if lc := newOrgLimiter(4, 99); lc.perOrg != 4 {
		t.Fatalf("perOrg must clamp to the global cap, got %d", lc.perOrg)
	}
	if lz := newOrgLimiter(0, 0); cap(lz.global) != 1 || lz.perOrg != 1 {
		t.Fatalf("zero caps must floor to 1, got global=%d perOrg=%d", cap(lz.global), lz.perOrg)
	}
}

// ── delta review: shed ordering (M-1) + async recover (M-2) ─────────────────

// TestSlackShedReturnsNon2xxAndDoesNotRecord proves Red M-1: when the per-org pool
// is saturated the webhook SHEDS the turn — returning a retriable NON-2xx and
// recording NOTHING — so Slack re-delivers when a slot frees and the @mention is
// never silently lost to a burned dedupe key. Uses a deterministic cap-1 limiter.
func TestSlackShedReturnsNon2xxAndDoesNotRecord(t *testing.T) {
	slackConfiguredEnv(t)
	t.Setenv("SLACK_SIGNING_SECRET", "shed-secret")
	authCh := make(chan string, 2)
	stubSlackBridge(t, authCh)
	app := newApp(t, newKMS(t))
	mounted.slackBridgeReady()

	// Swap in a cap-1 limiter, then saturate it so the next handler acquire sheds.
	saved := slackLim
	slackLim = newOrgLimiter(1, 1)
	t.Cleanup(func() { slackLim = saved })

	if cb := connectSlack(t, app, "shedorg", "acmecode"); cb.Code != http.StatusFound {
		t.Fatalf("connect: %d (%s)", cb.Code, cb.Body)
	}
	if !slackLim.acquire("shedorg") {
		t.Fatal("precondition: fill the cap-1 pool")
	}

	body := `{"type":"event_callback","team_id":"TACME","event_id":"EvShed","event":{"type":"app_mention","user":"Ushed","text":"<@BACME> hi","channel":"C1","ts":"1.1"}}`
	res := slackPost(t, app, "/v1/integrations/slack/events", "shed-secret", "application/json", body)
	if res.Code == http.StatusOK || res.Code < 400 {
		t.Fatalf("a shed turn must return a retriable NON-2xx so Slack re-delivers, got %d", res.Code)
	}
	// The dedupe key was NOT burned: marking it now must be FRESH — else a later
	// retry would be deduped away and the message lost forever.
	if fresh, err := mounted.store.MarkSlackEvent(context.Background(), "EvShed"); err != nil || !fresh {
		t.Fatalf("shed must NOT record the event_id (fresh=%v err=%v)", fresh, err)
	}
	// And no reply was posted (the turn never ran).
	select {
	case a := <-authCh:
		t.Fatalf("a shed turn must post no reply, saw %q", a)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestSlackTurnPanicRecoveredAndSlotReleased proves Red M-2: a panic in the async
// agent turn is CONTAINED (never crashes the shared multi-tenant process) AND the
// pool slot is released. Uses a deterministic cap-1 limiter; if recover failed the
// panic would abort the whole test binary.
func TestSlackTurnPanicRecoveredAndSlotReleased(t *testing.T) {
	slackConfiguredEnv(t)
	newApp(t, newKMS(t))
	mounted.slackBridgeReady()

	saved := slackLim
	slackLim = newOrgLimiter(1, 1)
	t.Cleanup(func() { slackLim = saved })

	const org = "panicorg"
	// Simulate the handler acquiring the single slot, then hand a PANICKING turn to
	// slackSpawn (which owns the release).
	if !slackLim.acquire(org) {
		t.Fatal("precondition: acquire the single slot")
	}
	mounted.slackSpawn(org, func() { panic("boom in a slack turn") })

	// The recovered goroutine must release its slot; poll until a fresh acquire
	// succeeds. Reaching here at all proves the panic did not crash the process.
	released := false
	for i := 0; i < 400; i++ {
		if slackLim.acquire(org) {
			slackLim.release(org)
			released = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !released {
		t.Fatal("a panicking turn must release its slot (and never crash the shared process)")
	}
}
