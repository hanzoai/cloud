package integrations

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// telegram_link.go is Telegram's per-user account link: a transplant-safe browser
// flow whose PLATFORM-IDENTIFY leg is the Telegram Login Widget (Telegram
// cryptographically signs the auth data with the bot token, so we KNOW the real
// Telegram user), followed by the shared hanzo.id OIDC leg (bridge_link.go):
//
//	GET /v1/integrations/telegram/link           leg1: set __Host init+link cookies, render the Login Widget
//	GET /v1/integrations/telegram/link/auth       leg2: widget callback; verify the signed auth, bind (chat,tgUser)
//	GET /v1/integrations/telegram/link/callback   leg3: hanzo.id OIDC callback; bind Telegram↔Hanzo, seal refresh
//
// The bound Telegram user is NEVER a URL param — it comes ONLY from the
// widget-signed auth data (leg2), exactly as Slack takes it from the Slack sign-in.
// A forwarded link therefore cannot bind a victim's account: whoever completes the
// widget links only THEIR own (widget-verified) Telegram id.

const (
	telegramInitCookie = "__Host-hanzo_tg_init"
	telegramLinkCookie = "__Host-hanzo_tg_link"
	telegramOIDCCookie = "__Host-hanzo_tg_oidc"
	// telegramLoginMaxAgeSec bounds how old the widget auth_date may be — an old
	// captured auth blob is rejected even if its signature is valid.
	telegramLoginMaxAgeSec = 86400
)

// telegramLinkConfigured reports whether the full per-user link can run: the bot
// (token+username, for the widget) plus the shared hanzo.id confidential client.
func telegramLinkConfigured() bool {
	return telegramConfigured() && telegramBotUsername() != "" && oidcConfigured()
}

// telegramLinkURL builds the per-user "connect your Hanzo account" URL. The chat id
// is PROVENANCE (→ org at redemption); the Telegram user is taken from the widget,
// never this URL. Returns an error when linking isn't configured → the brain shows a
// URL-less prompt (never a dead-end).
func telegramLinkURL(s *cloud.Service[state], chatID, _user string) (string, error) {
	if !telegramLinkConfigured() {
		return "", fmt.Errorf("telegram: account linking not configured")
	}
	state, err := signSubject(s.State.stateKey, "tgentry:"+chatID, 0)
	if err != nil {
		return "", err
	}
	return "https://" + s.Domain + "/v1/integrations/telegram/link?state=" + url.QueryEscape(state), nil
}

// ── leg 1: render the Telegram Login Widget ─────────────────────────────────

func telegramLink(s *cloud.Service[state], c *zip.Ctx) error {
	bridgeReady()
	if !telegramLinkConfigured() {
		return zip.Errorf(http.StatusServiceUnavailable, "account linking not configured")
	}
	subject, _, ok := verifySubject(s.State.stateKey, c.Query("state"), 0)
	if !ok || !strings.HasPrefix(subject, "tgentry:") {
		return zip.ErrBadRequest("invalid or expired link")
	}
	chatID := strings.TrimPrefix(subject, "tgentry:")
	nonce, err := genToken()
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	linkVal, err := signSubject(s.State.stateKey, "tgchat:"+chatID+":"+nonce, 0)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "state: %v", err)
	}
	setLinkCookie(c, telegramInitCookie, nonce)
	setLinkCookie(c, telegramLinkCookie, linkVal)
	c.Fiber().Status(http.StatusOK).Type("html")
	return c.Fiber().SendString(telegramWidgetHTML(telegramBotUsername(), "https://"+s.Domain+"/v1/integrations/telegram/link/auth"))
}

// telegramWidgetHTML renders the Login Widget. The bot's domain must be set to this
// deployment via @BotFather /setdomain for the widget to appear. On login Telegram
// GET-redirects the top-level browser to authURL with the signed auth params.
func telegramWidgetHTML(botUsername, authURL string) string {
	return `<!doctype html><meta charset="utf-8"><title>Connect Hanzo</title>` +
		`<body style="font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;text-align:center">` +
		`<h1>Connect your Hanzo account</h1>` +
		`<p>Sign in with Telegram to link @hanzo to your Hanzo account.</p>` +
		`<script async src="https://telegram.org/js/telegram-widget.js?22" ` +
		`data-telegram-login="` + botUsername + `" data-size="large" ` +
		`data-auth-url="` + authURL + `" data-request-access="write"></script></body>`
}

// ── leg 2: Login Widget callback (verify the signed Telegram identity) ──────

func telegramLinkAuth(s *cloud.Service[state], c *zip.Ctx) error {
	bridgeReady()
	if !telegramLinkConfigured() {
		return zip.Errorf(http.StatusServiceUnavailable, "account linking not configured")
	}
	// Verify the widget's signed auth data with the bot token → the REAL Telegram
	// user id. This is the identity source (never a URL/state param).
	tgUser, ok := verifyTelegramLogin(telegramBotToken(), c.Fiber().Queries(), 0)
	if !ok {
		clearTelegramLinkCookies(c)
		return zip.ErrBadRequest("telegram sign-in could not be verified")
	}
	link := readLinkCookie(c, telegramLinkCookie)
	chatSub, _, lok := verifySubject(s.State.stateKey, link, 0)
	if !lok || !strings.HasPrefix(chatSub, "tgchat:") {
		clearTelegramLinkCookies(c)
		return zip.ErrBadRequest("link session missing; restart from Telegram")
	}
	parts := strings.SplitN(strings.TrimPrefix(chatSub, "tgchat:"), ":", 2)
	if len(parts) != 2 {
		clearTelegramLinkCookies(c)
		return zip.ErrBadRequest("invalid link session")
	}
	chatID, nonce := parts[0], parts[1]
	// Browser continuity (transplant defense): the init cookie MUST equal the nonce
	// carried in the (signed) link cookie.
	if init := readLinkCookie(c, telegramInitCookie); init == "" || init != nonce {
		clearTelegramLinkCookies(c)
		return zip.ErrBadRequest("link session mismatch; restart from Telegram")
	}
	if _, ok := OrgForExternalID("telegram", chatID); !ok {
		clearTelegramLinkCookies(c)
		return zip.ErrBadRequest("chat not connected")
	}
	oidcVal, err := signSubject(s.State.stateKey, "tglink:"+chatID+":"+tgUser, 0)
	if err != nil {
		clearTelegramLinkCookies(c)
		return zip.Errorf(http.StatusInternalServerError, "link state: %v", err)
	}
	clearLinkCookie(c, telegramInitCookie)
	clearLinkCookie(c, telegramLinkCookie)
	setLinkCookie(c, telegramOIDCCookie, oidcVal)
	return redirect(c, oidcAuthorizeURL(telegramLinkCallbackURI(s), oidcVal))
}

// ── leg 3: hanzo.id OIDC callback (bind Telegram↔Hanzo) ─────────────────────

func telegramLinkCallback(s *cloud.Service[state], c *zip.Ctx) error {
	bridgeReady()
	if !telegramLinkConfigured() {
		return zip.Errorf(http.StatusServiceUnavailable, "account linking not configured")
	}
	cookie := readLinkCookie(c, telegramOIDCCookie)
	if e := strings.TrimSpace(c.Query("error")); e != "" {
		clearLinkCookie(c, telegramOIDCCookie)
		return zip.ErrBadRequest("authorization declined")
	}
	code := strings.TrimSpace(c.Query("code"))
	state := c.Query("state")
	if code == "" {
		clearLinkCookie(c, telegramOIDCCookie)
		return zip.ErrBadRequest("missing code")
	}
	if len(code) > maxCodeLen {
		clearLinkCookie(c, telegramOIDCCookie)
		return zip.ErrBadRequest("authorization code too large")
	}
	if cookie == "" {
		return zip.ErrBadRequest("link session missing; restart from Telegram")
	}
	if state == "" || state != cookie {
		clearLinkCookie(c, telegramOIDCCookie)
		return zip.ErrBadRequest("link state mismatch")
	}
	sub2, nonce, ok := verifySubject(s.State.stateKey, cookie, 0)
	if !ok || !strings.HasPrefix(sub2, "tglink:") {
		clearLinkCookie(c, telegramOIDCCookie)
		return zip.ErrBadRequest("invalid or expired link")
	}
	parts := strings.SplitN(strings.TrimPrefix(sub2, "tglink:"), ":", 2)
	if len(parts) != 2 {
		clearLinkCookie(c, telegramOIDCCookie)
		return zip.ErrBadRequest("invalid link session")
	}
	chatID, tgUser := parts[0], parts[1]
	org, iok := OrgForExternalID("telegram", chatID)
	if !iok {
		clearLinkCookie(c, telegramOIDCCookie)
		return zip.ErrBadRequest("chat not connected")
	}
	if !kmsReady(s) {
		clearLinkCookie(c, telegramOIDCCookie)
		return zip.Errorf(http.StatusServiceUnavailable, "secret store unavailable")
	}
	ctx := c.Context()
	ts, err := oidcExchange(ctx, code, telegramLinkCallbackURI(s))
	if err != nil {
		s.Log.Error("telegram: link code exchange", "chat", chatID, "err", err)
		clearLinkCookie(c, telegramOIDCCookie)
		return zip.ErrBadRequest("link failed")
	}
	// Single-use AFTER a successful exchange (a bogus code cannot burn a valid nonce).
	if bridgeSeen.seenAndAdd(nonce, time.Time{}) {
		clearLinkCookie(c, telegramOIDCCookie)
		return zip.ErrBadRequest("link already used")
	}
	if ts.Refresh == "" {
		clearLinkCookie(c, telegramOIDCCookie)
		return zip.Errorf(http.StatusInternalServerError, "link incomplete: no refresh token")
	}
	sub, userOrg, err := oidcIdentity(ctx, ts.Access)
	if err != nil {
		s.Log.Error("telegram: link identity", "chat", chatID, "err", err)
		clearLinkCookie(c, telegramOIDCCookie)
		return zip.Errorf(http.StatusInternalServerError, "link identity")
	}
	if err := putUserLink(s, org, "telegram", tgUser, userLink{Subject: sub, Org: userOrg, Refresh: ts.Refresh}); err != nil {
		s.Log.Error("telegram: user link save", "chat", chatID, "err", err) // never logs the token
		clearLinkCookie(c, telegramOIDCCookie)
		return zip.Errorf(http.StatusInternalServerError, "token store failed")
	}
	clearLinkCookie(c, telegramOIDCCookie)
	s.Log.Info("telegram: user linked", "chat", chatID, "tgUser", tgUser) // no token
	c.Fiber().Status(http.StatusOK).Type("html")
	return c.Fiber().SendString(bridgeLinkedHTML("Telegram"))
}

func clearTelegramLinkCookies(c *zip.Ctx) {
	clearLinkCookie(c, telegramInitCookie)
	clearLinkCookie(c, telegramLinkCookie)
}

func telegramLinkCallbackURI(s *cloud.Service[state]) string {
	return "https://" + s.Domain + "/v1/integrations/telegram/link/callback"
}

// ── Telegram Login Widget signature verification ────────────────────────────

// verifyTelegramLogin verifies the Login Widget auth data. Telegram signs it as
//
//	hash = hex(HMAC_SHA256(SHA256(bot_token), data_check_string))
//
// where data_check_string is every field EXCEPT hash, as "key=value" lines sorted
// by key and joined with "\n". Returns the verified Telegram user id (the `id`
// field) iff the MAC matches AND auth_date is fresh. `now`=0 → time.Now.
// https://core.telegram.org/widgets/login#checking-authorization
func verifyTelegramLogin(botToken string, q map[string]string, now int64) (userID string, ok bool) {
	if botToken == "" {
		return "", false
	}
	got := q["hash"]
	if got == "" || q["id"] == "" {
		return "", false
	}
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
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(got), []byte(expected)) {
		return "", false
	}
	// Freshness: reject a stale (replayed) auth blob.
	if ad, err := strconv.ParseInt(q["auth_date"], 10, 64); err == nil {
		if now == 0 {
			now = time.Now().Unix()
		}
		if abs64(now-ad) > telegramLoginMaxAgeSec {
			return "", false
		}
	} else {
		return "", false
	}
	return q["id"], true
}
