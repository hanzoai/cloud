package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// discord_link.go is Discord's per-user account link: the PLATFORM-IDENTIFY leg is
// Discord OAuth2 `identify` (Discord proves which Discord user is linking), then the
// shared hanzo.id OIDC leg (bridge_link.go):
//
//	GET /v1/integrations/discord/link           leg1: set __Host init cookie, redirect to Discord identify
//	GET /v1/integrations/discord/link/discord    leg2: Discord callback; verify user, bind (guild,discordUser)
//	GET /v1/integrations/discord/link/callback    leg3: hanzo.id OIDC callback; bind Discord↔Hanzo, seal refresh
//
// The bound Discord user comes ONLY from the identify exchange (Discord-verified),
// never a URL param — a forwarded link can only bind the clicker's own account.

const (
	discordInitCookie = "__Host-hanzo_dsc_init"
	discordOIDCCookie = "__Host-hanzo_dsc_oidc"
)

func discordLinkConfigured() bool { return discordConfigured() && oidcConfigured() }

// discordLinkURL builds the per-user link URL. The guild id is PROVENANCE (→ org at
// redemption); the Discord user is taken from the identify leg, never this URL.
func discordLinkURL(s *cloud.Service[state], guildID, _user string) (string, error) {
	if !discordLinkConfigured() {
		return "", fmt.Errorf("discord: account linking not configured")
	}
	state, err := signSubject(s.State.stateKey, "dscentry:"+guildID, 0)
	if err != nil {
		return "", err
	}
	return "https://" + s.Domain + "/v1/integrations/discord/link?state=" + url.QueryEscape(state), nil
}

// ── leg 1: redirect to Discord identify ─────────────────────────────────────

func discordLink(s *cloud.Service[state], c *zip.Ctx) error {
	bridgeReady()
	if !discordLinkConfigured() {
		return zip.Errorf(http.StatusServiceUnavailable, "account linking not configured")
	}
	subject, _, ok := verifySubject(s.State.stateKey, c.Query("state"), 0)
	if !ok || !strings.HasPrefix(subject, "dscentry:") {
		return zip.ErrBadRequest("invalid or expired link")
	}
	guildID := strings.TrimPrefix(subject, "dscentry:")
	nonce, err := genToken()
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	state, err := signSubject(s.State.stateKey, "dsc:"+guildID+":"+nonce, 0)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "state: %v", err)
	}
	setLinkCookie(c, discordInitCookie, nonce)
	q := url.Values{
		"client_id":     {discordClientID()},
		"scope":         {"identify"},
		"response_type": {"code"},
		"redirect_uri":  {discordLinkDiscordURI(s)},
		"state":         {state},
		"prompt":        {"none"},
	}
	return redirect(c, discordOAuthAuthorizeURL+"?"+q.Encode())
}

// ── leg 2: Discord identify callback ────────────────────────────────────────

func discordLinkDiscord(s *cloud.Service[state], c *zip.Ctx) error {
	bridgeReady()
	if !discordLinkConfigured() {
		return zip.Errorf(http.StatusServiceUnavailable, "account linking not configured")
	}
	if e := strings.TrimSpace(c.Query("error")); e != "" {
		clearLinkCookie(c, discordInitCookie)
		return zip.ErrBadRequest("discord authorization declined")
	}
	code := strings.TrimSpace(c.Query("code"))
	state := c.Query("state")
	if code == "" || state == "" {
		return zip.ErrBadRequest("missing code or state")
	}
	if len(code) > maxCodeLen {
		return zip.ErrBadRequest("authorization code too large")
	}
	subject, _, ok := verifySubject(s.State.stateKey, state, 0)
	if !ok || !strings.HasPrefix(subject, "dsc:") {
		clearLinkCookie(c, discordInitCookie)
		return zip.ErrBadRequest("invalid or expired link")
	}
	parts := strings.SplitN(strings.TrimPrefix(subject, "dsc:"), ":", 2)
	if len(parts) != 2 {
		clearLinkCookie(c, discordInitCookie)
		return zip.ErrBadRequest("invalid link session")
	}
	guildID, nonce := parts[0], parts[1]
	if init := readLinkCookie(c, discordInitCookie); init == "" || init != nonce {
		clearLinkCookie(c, discordInitCookie)
		return zip.ErrBadRequest("link session mismatch; restart from Discord")
	}
	discordUser, err := discordExchangeIdentify(c.Context(), code, discordLinkDiscordURI(s))
	if err != nil {
		s.Log.Error("discord: identify exchange", "err", err)
		clearLinkCookie(c, discordInitCookie)
		return zip.ErrBadRequest("discord sign-in failed")
	}
	if _, ok := OrgForExternalID("discord", guildID); !ok {
		clearLinkCookie(c, discordInitCookie)
		return zip.ErrBadRequest("server not connected")
	}
	oidcVal, err := signSubject(s.State.stateKey, "dsclink:"+guildID+":"+discordUser, 0)
	if err != nil {
		clearLinkCookie(c, discordInitCookie)
		return zip.Errorf(http.StatusInternalServerError, "link state: %v", err)
	}
	clearLinkCookie(c, discordInitCookie)
	setLinkCookie(c, discordOIDCCookie, oidcVal)
	return redirect(c, oidcAuthorizeURL(discordLinkCallbackURI(s), oidcVal))
}

// ── leg 3: hanzo.id OIDC callback (bind Discord↔Hanzo) ──────────────────────

func discordLinkCallback(s *cloud.Service[state], c *zip.Ctx) error {
	bridgeReady()
	if !discordLinkConfigured() {
		return zip.Errorf(http.StatusServiceUnavailable, "account linking not configured")
	}
	cookie := readLinkCookie(c, discordOIDCCookie)
	if e := strings.TrimSpace(c.Query("error")); e != "" {
		clearLinkCookie(c, discordOIDCCookie)
		return zip.ErrBadRequest("authorization declined")
	}
	code := strings.TrimSpace(c.Query("code"))
	state := c.Query("state")
	if code == "" {
		clearLinkCookie(c, discordOIDCCookie)
		return zip.ErrBadRequest("missing code")
	}
	if len(code) > maxCodeLen {
		clearLinkCookie(c, discordOIDCCookie)
		return zip.ErrBadRequest("authorization code too large")
	}
	if cookie == "" {
		return zip.ErrBadRequest("link session missing; restart from Discord")
	}
	if state == "" || state != cookie {
		clearLinkCookie(c, discordOIDCCookie)
		return zip.ErrBadRequest("link state mismatch")
	}
	subject, nonce, ok := verifySubject(s.State.stateKey, cookie, 0)
	if !ok || !strings.HasPrefix(subject, "dsclink:") {
		clearLinkCookie(c, discordOIDCCookie)
		return zip.ErrBadRequest("invalid or expired link")
	}
	parts := strings.SplitN(strings.TrimPrefix(subject, "dsclink:"), ":", 2)
	if len(parts) != 2 {
		clearLinkCookie(c, discordOIDCCookie)
		return zip.ErrBadRequest("invalid link session")
	}
	guildID, discordUser := parts[0], parts[1]
	org, iok := OrgForExternalID("discord", guildID)
	if !iok {
		clearLinkCookie(c, discordOIDCCookie)
		return zip.ErrBadRequest("server not connected")
	}
	if !kmsReady(s) {
		clearLinkCookie(c, discordOIDCCookie)
		return zip.Errorf(http.StatusServiceUnavailable, "secret store unavailable")
	}
	ctx := c.Context()
	ts, err := oidcExchange(ctx, code, discordLinkCallbackURI(s))
	if err != nil {
		s.Log.Error("discord: link code exchange", "guild", guildID, "err", err)
		clearLinkCookie(c, discordOIDCCookie)
		return zip.ErrBadRequest("link failed")
	}
	if bridgeSeen.seenAndAdd(nonce, time.Time{}) {
		clearLinkCookie(c, discordOIDCCookie)
		return zip.ErrBadRequest("link already used")
	}
	if ts.Refresh == "" {
		clearLinkCookie(c, discordOIDCCookie)
		return zip.Errorf(http.StatusInternalServerError, "link incomplete: no refresh token")
	}
	sub, userOrg, err := oidcIdentity(ctx, ts.Access)
	if err != nil {
		s.Log.Error("discord: link identity", "guild", guildID, "err", err)
		clearLinkCookie(c, discordOIDCCookie)
		return zip.Errorf(http.StatusInternalServerError, "link identity")
	}
	if err := putUserLink(s, org, "discord", discordUser, userLink{Subject: sub, Org: userOrg, Refresh: ts.Refresh}); err != nil {
		s.Log.Error("discord: user link save", "guild", guildID, "err", err) // never logs the token
		clearLinkCookie(c, discordOIDCCookie)
		return zip.Errorf(http.StatusInternalServerError, "token store failed")
	}
	clearLinkCookie(c, discordOIDCCookie)
	s.Log.Info("discord: user linked", "guild", guildID, "discordUser", discordUser) // no token
	c.Fiber().Status(http.StatusOK).Type("html")
	return c.Fiber().SendString(bridgeLinkedHTML("Discord"))
}

func discordLinkDiscordURI(s *cloud.Service[state]) string {
	return "https://" + s.Domain + "/v1/integrations/discord/link/discord"
}

func discordLinkCallbackURI(s *cloud.Service[state]) string {
	return "https://" + s.Domain + "/v1/integrations/discord/link/callback"
}

// ── Discord identify (user_scope) code exchange ─────────────────────────────

// discordExchangeIdentify trades the identify code for the VERIFIED Discord user
// id via oauth2/token + GET /users/@me. The identity is proven by Discord here,
// never taken from a URL/state param.
func discordExchangeIdentify(ctx context.Context, code, redirectURI string) (discordUser string, err error) {
	form := url.Values{
		"client_id":     {discordClientID()},
		"client_secret": {discordClientSecret()},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, discordAPIBase+"/oauth2/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := bridgeHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, bridgeMaxBody))
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("discord token http %d", resp.StatusCode)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", fmt.Errorf("discord token decode: %w", err)
	}
	if tok.Error != "" || tok.AccessToken == "" {
		return "", fmt.Errorf("discord token: %s", nonEmpty(tok.Error, "no access_token"))
	}
	meReq, err := http.NewRequestWithContext(ctx, http.MethodGet, discordAPIBase+"/users/@me", nil)
	if err != nil {
		return "", err
	}
	meReq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	meResp, err := bridgeHTTP.Do(meReq)
	if err != nil {
		return "", err
	}
	defer func() { _ = meResp.Body.Close() }()
	if meResp.StatusCode/100 != 2 {
		return "", fmt.Errorf("discord /users/@me http %d", meResp.StatusCode)
	}
	var me struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(meResp.Body, bridgeMaxBody)).Decode(&me); err != nil {
		return "", err
	}
	if me.ID == "" {
		return "", fmt.Errorf("discord /users/@me: no id")
	}
	return me.ID, nil
}
