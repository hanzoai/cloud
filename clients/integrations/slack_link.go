package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/hanzoai/cloud/clients/kms"
	"github.com/zap-proto/zip"
)

// slack_link.go is the per-user account link — the transplant-safe browser flow
// (ported verbatim from team-go/pkg/slack) that binds a Slack user to their own
// Hanzo account so @hanzo runs ON BEHALF OF them:
//
//	GET /v1/integrations/slack/link           leg1: set __Host- init cookie + redirect to Slack sign-in
//	GET /v1/integrations/slack/link/slack     leg2: Slack sign-in callback; require init-cookie==state BEFORE exchange
//	GET /v1/integrations/slack/link/callback  leg3: hanzo.id OIDC callback; bind Slack↔Hanzo, seal refresh in KMS
//
// TRANSPLANT DEFENSE. The binding identity is NEVER a URL/state param. Leg1 mints
// a random nonce carried BOTH in an httpOnly __Host- init cookie AND the
// slack-signin state's subject; leg2 refuses unless they match (a code+state
// pasted into a VICTIM's browser carries no matching init cookie). Leg2 then sets
// a __Host- link cookie carrying the SLACK-VERIFIED (team,user); leg3 reads the
// (team,user) from THAT cookie (a forwarded link carries no cookie → cannot bind)
// and requires the OIDC state to equal the cookie. The __Host- prefix host-binds
// both cookies (Secure + no Domain + Path=/), so a sibling *.hanzo.ai origin
// cannot plant/fixate a same-named cookie.

// ── cookies ──────────────────────────────────────────────────────────────────

const (
	// slackInitCookie ties leg1→leg2 in ONE browser: its random value is ALSO the
	// subject of the slack-signin state, and leg2 requires cookie==subject.
	slackInitCookie = "__Host-hanzo_slack_init"
	// slackLinkCookie carries the SLACK-VERIFIED (team,user) across the hanzo.id
	// OIDC leg. The binding is read from THIS cookie, never a URL/state param.
	slackLinkCookie = "__Host-hanzo_slack_link"
)

// setSlackCookie writes a browser-bound, httpOnly __Host- cookie. SameSite=Lax so
// it survives the top-level GET redirect back from Slack / hanzo.id; Secure +
// no Domain + Path=/ satisfy the __Host- prefix (net/http Cookie.Valid enforces
// this, else fiber drops the cookie).
func (s *svc) setSlackCookie(c *zip.Ctx, name, val string) {
	c.Fiber().Cookie(&fiber.Cookie{
		Name: name, Value: val, Path: "/",
		MaxAge: slackLinkTTLSec, HTTPOnly: true, Secure: true,
		SameSite: fiber.CookieSameSiteLaxMode,
	})
}

func (s *svc) readSlackCookie(c *zip.Ctx, name string) string { return c.Fiber().Cookies(name) }

func (s *svc) clearSlackCookie(c *zip.Ctx, name string) {
	c.Fiber().Cookie(&fiber.Cookie{
		Name: name, Value: "", Path: "/",
		MaxAge: -1, HTTPOnly: true, Secure: true,
		SameSite: fiber.CookieSameSiteLaxMode,
	})
}

// ── leg 1: begin the per-user link (Slack sign-in) ──────────────────────────

func (s *svc) slackLink(c *zip.Ctx) error {
	s.slackBridgeReady()
	if !s.slackLinkConfigured() {
		return zip.Errorf(http.StatusServiceUnavailable, "account linking not configured")
	}
	// The entry state is PROVENANCE only (proves a server-minted prompt); the
	// binding identity is taken from the Slack sign-in leg, not from it. Reject a
	// missing/forged entry state.
	if _, _, _, ok := verifySlackLink(s.stateKey, c.Query("state"), 0); !ok {
		return zip.ErrBadRequest("invalid or expired link")
	}
	initNonce, err := genToken()
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	ss, err := signSlackSubject(s.stateKey, initNonce, 0)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "state: %v", err)
	}
	s.setSlackCookie(c, slackInitCookie, initNonce)
	return redirect(c, s.slackUserAuthorizeURL(ss))
}

// slackUserAuthorizeURL builds the Slack sign-in (user_scope) authorize redirect.
func (s *svc) slackUserAuthorizeURL(state string) string {
	q := url.Values{
		"client_id":    {slackCreds().ClientID},
		"user_scope":   {"openid"},
		"redirect_uri": {s.slackLinkSlackURI()},
		"state":        {state},
	}
	return slackAuthorizeURL + "?" + q.Encode()
}

// ── leg 2: Slack sign-in callback ───────────────────────────────────────────

func (s *svc) slackLinkSlack(c *zip.Ctx) error {
	s.slackBridgeReady()
	if !s.slackLinkConfigured() {
		return zip.Errorf(http.StatusServiceUnavailable, "account linking not configured")
	}
	if e := strings.TrimSpace(c.Query("error")); e != "" {
		s.clearSlackCookie(c, slackInitCookie)
		return zip.ErrBadRequest("slack authorization declined")
	}
	code := strings.TrimSpace(c.Query("code"))
	state := c.Query("state")
	if code == "" || state == "" {
		return zip.ErrBadRequest("missing code or state")
	}
	if len(code) > maxCodeLen {
		return zip.ErrBadRequest("authorization code too large")
	}
	subject, _, ok := verifySlackSubject(s.stateKey, state, 0)
	if !ok {
		s.clearSlackCookie(c, slackInitCookie)
		return zip.ErrBadRequest("invalid or expired link")
	}
	// Browser continuity (transplant defense): the leg-1 init cookie MUST equal the
	// nonce carried in the slack-signin state's subject. A transplanted link has no
	// matching init cookie → refuse BEFORE exchanging the code or planting a cookie.
	if init := s.readSlackCookie(c, slackInitCookie); init == "" || init != subject {
		s.clearSlackCookie(c, slackInitCookie)
		return zip.ErrBadRequest("link session mismatch; restart from Slack")
	}
	// Single-use: consume the SAME continuity token the gate compared (subject ==
	// leg-1 init nonce == init cookie value), so the gate token and single-use token
	// are ONE value that cannot desync.
	if slackUsedStates.seenAndAdd(subject, time.Time{}) {
		s.clearSlackCookie(c, slackInitCookie)
		return zip.ErrBadRequest("link already used")
	}
	teamID, slackUser, err := slackExchangeUser(c.Context(), slackCreds(), s.slackLinkSlackURI(), code)
	if err != nil {
		s.log.Error("slack: user auth exchange", "err", err)
		s.clearSlackCookie(c, slackInitCookie)
		return zip.ErrBadRequest("slack sign-in failed")
	}
	if _, ok := OrgForExternalID("slack", teamID); !ok {
		s.clearSlackCookie(c, slackInitCookie)
		return zip.ErrBadRequest("workspace not connected")
	}
	cookieVal, err := signSlackLink(s.stateKey, teamID, slackUser, 0)
	if err != nil {
		s.clearSlackCookie(c, slackInitCookie)
		return zip.Errorf(http.StatusInternalServerError, "link state: %v", err)
	}
	// The init cookie has done its job; the link cookie now carries the
	// Slack-verified (team,user) across the hanzo.id leg.
	s.clearSlackCookie(c, slackInitCookie)
	s.setSlackCookie(c, slackLinkCookie, cookieVal)
	// hanzo.id authorize state == the cookie value (ties this OIDC leg to THIS
	// browser; leg3 requires state == cookie).
	return redirect(c, s.slackOIDCAuthorizeURL(cookieVal))
}

// ── leg 3: hanzo.id OIDC callback (bind Slack↔Hanzo) ────────────────────────

// slackLinkedHTML is the terse success page shown after a user links their account.
const slackLinkedHTML = `<!doctype html><meta charset="utf-8"><title>Hanzo connected</title>` +
	`<body style="font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;text-align:center">` +
	`<h1>Hanzo connected</h1><p>Your Hanzo account is linked. Return to Slack and mention <b>@hanzo</b>.</p></body>`

func (s *svc) slackLinkCallback(c *zip.Ctx) error {
	s.slackBridgeReady()
	if !s.slackLinkConfigured() {
		return zip.Errorf(http.StatusServiceUnavailable, "account linking not configured")
	}
	cookie := s.readSlackCookie(c, slackLinkCookie)
	if e := strings.TrimSpace(c.Query("error")); e != "" {
		s.clearSlackCookie(c, slackLinkCookie)
		return zip.ErrBadRequest("authorization declined")
	}
	code := strings.TrimSpace(c.Query("code"))
	state := c.Query("state")
	if code == "" {
		s.clearSlackCookie(c, slackLinkCookie)
		return zip.ErrBadRequest("missing code")
	}
	if len(code) > maxCodeLen {
		s.clearSlackCookie(c, slackLinkCookie)
		return zip.ErrBadRequest("authorization code too large")
	}
	// The binding subject is the browser-bound cookie (set only after a
	// Slack-verified sign-in). No cookie → refuse (link-hijack defense).
	if cookie == "" {
		return zip.ErrBadRequest("link session missing; restart from Slack")
	}
	// state must equal the cookie (OIDC CSRF + browser binding).
	if state == "" || state != cookie {
		s.clearSlackCookie(c, slackLinkCookie)
		return zip.ErrBadRequest("link state mismatch")
	}
	teamID, slackUser, nonce, ok := verifySlackLink(s.stateKey, cookie, 0)
	if !ok {
		s.clearSlackCookie(c, slackLinkCookie)
		return zip.ErrBadRequest("invalid or expired link")
	}
	org, iok := OrgForExternalID("slack", teamID)
	if !iok {
		s.clearSlackCookie(c, slackLinkCookie)
		return zip.ErrBadRequest("workspace not connected")
	}
	if !s.kmsReady() {
		s.clearSlackCookie(c, slackLinkCookie)
		return zip.Errorf(http.StatusServiceUnavailable, "secret store unavailable")
	}
	ctx := c.Context()
	ts, err := s.slackOIDCExchange(ctx, code)
	if err != nil {
		s.log.Error("slack: link code exchange", "team", teamID, "err", err)
		s.clearSlackCookie(c, slackLinkCookie)
		return zip.ErrBadRequest("link failed")
	}
	// Consume the single-use nonce AFTER a successful exchange (anti-griefing: a
	// bogus code cannot burn a valid nonce).
	if slackUsedStates.seenAndAdd(nonce, time.Time{}) {
		s.clearSlackCookie(c, slackLinkCookie)
		return zip.ErrBadRequest("link already used")
	}
	if ts.Refresh == "" {
		s.clearSlackCookie(c, slackLinkCookie)
		return zip.Errorf(http.StatusInternalServerError, "link incomplete: no refresh token")
	}
	sub, userOrg, err := s.slackOIDCIdentity(ctx, ts.Access)
	if err != nil {
		s.log.Error("slack: link identity", "team", teamID, "err", err)
		s.clearSlackCookie(c, slackLinkCookie)
		return zip.Errorf(http.StatusInternalServerError, "link identity")
	}
	// Seal the binding under the WORKSPACE org's KMS namespace (the same org the
	// bot token lives under). The (team,user) is the Slack-verified cookie subject;
	// the refresh token is NEVER a DB column and NEVER logged.
	if err := s.putSlackUserLink(org, slackUser, slackUserLink{Subject: sub, Org: userOrg, Refresh: ts.Refresh}); err != nil {
		s.log.Error("slack: user link save", "team", teamID, "err", err) // never logs the token
		s.clearSlackCookie(c, slackLinkCookie)
		return zip.Errorf(http.StatusInternalServerError, "token store failed")
	}
	s.clearSlackCookie(c, slackLinkCookie)
	s.log.Info("slack: user linked", "team", teamID, "slackUser", slackUser) // no token
	c.Fiber().Status(http.StatusOK).Type("html")
	return c.Fiber().SendString(slackLinkedHTML)
}

// slackLinkURL builds the per-user "connect your Hanzo account" URL: the /link
// entry carrying a signed, single-use state binding (team,user). The (team,user)
// is PROVENANCE only — redemption takes the account binding from the Slack-verified
// sign-in + browser cookie, never from this URL.
func (s *svc) slackLinkURL(teamID, slackUser string) (string, error) {
	state, err := signSlackLink(s.stateKey, teamID, slackUser, 0)
	if err != nil {
		return "", err
	}
	return "https://" + s.domain + "/v1/integrations/slack/link?state=" + url.QueryEscape(state), nil
}

// ── per-user Hanzo binding (KMS-custodied, per-org) ─────────────────────────

const (
	slackUserSecretPrefix = "user:"
	slackUserSecretSuffix = ":refresh"
)

// slackUserSecretName is the KMS secret name for a linked Slack user's binding —
// "user:<slackUser>:refresh" under the org's integrations/slack namespace. Slack
// user ids are [A-Z0-9] (no '/', NUL, or control chars), so this is a valid KMS
// segment.
func slackUserSecretName(slackUser string) string {
	return slackUserSecretPrefix + slackUser + slackUserSecretSuffix
}

// slackUserLink is the on-behalf-of binding for a linked Slack user: the Hanzo
// account subject (what RunOnBehalf attributes the spend to), the account org, and
// the Hanzo refresh token (sealed for future token minting; the in-process run
// needs only the subject). Sealed KMS-encrypted; never a DB column, never logged.
type slackUserLink struct {
	Subject string `json:"subject"`
	Org     string `json:"org"`
	Refresh string `json:"refresh"`
}

func (s *svc) putSlackUserLink(org, slackUser string, link slackUserLink) error {
	if !validOrg(org) {
		return fmt.Errorf("slack: invalid org")
	}
	blob, err := json.Marshal(link)
	if err != nil {
		return err
	}
	return s.kmsPut(org, "slack", slackUserSecretName(slackUser), blob)
}

// getSlackUserLink returns the linked (org, slackUser) binding. found=false (nil
// error) when the user has not linked (no secret). Fails closed on invalid org /
// KMS-down.
func (s *svc) getSlackUserLink(org, slackUser string) (slackUserLink, bool, error) {
	if !validOrg(org) {
		return slackUserLink{}, false, fmt.Errorf("slack: invalid org")
	}
	if !s.kmsReady() {
		return slackUserLink{}, false, kms.ErrMasterKeyMissing
	}
	raw, err := s.kmsGet(org, "slack", slackUserSecretName(slackUser))
	if errors.Is(err, kms.ErrSecretNotFound) {
		return slackUserLink{}, false, nil
	}
	if err != nil {
		return slackUserLink{}, false, err
	}
	var link slackUserLink
	if err := json.Unmarshal(raw, &link); err != nil {
		return slackUserLink{}, false, fmt.Errorf("slack: user link decode: %w", err)
	}
	if link.Subject == "" {
		return slackUserLink{}, false, nil
	}
	return link, true, nil
}

// ── Slack sign-in (user_scope) code exchange ────────────────────────────────

// slackUserOAuthResponse is the oauth.v2.access response for the USER sign-in leg:
// authed_user.id is the Slack-VERIFIED subject; team.id the workspace.
type slackUserOAuthResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	Team  struct {
		ID string `json:"id"`
	} `json:"team"`
	AuthedUser struct {
		ID string `json:"id"`
	} `json:"authed_user"`
}

// slackExchangeUser trades the Slack sign-in code for the Slack-VERIFIED
// (team,user). It reuses the OAuth provider's slackPostForm + slackWebAPIBase so
// there is ONE Slack Web API path. The identity is proven by Slack here, NEVER
// taken from a client-supplied URL/state param (the account-link-hijack defense).
func slackExchangeUser(ctx context.Context, creds OAuthConfig, redirectURI, code string) (teamID, slackUser string, err error) {
	form := url.Values{
		"client_id":     {creds.ClientID},
		"client_secret": {creds.ClientSecret},
		"code":          {code},
		"redirect_uri":  {redirectURI},
	}
	var r slackUserOAuthResponse
	if err := slackPostForm(ctx, slackWebAPIBase+"/oauth.v2.access", form, &r); err != nil {
		return "", "", err
	}
	if !r.OK {
		return "", "", fmt.Errorf("slack oauth.v2.access error: %s", nonEmpty(r.Error, "unknown_error"))
	}
	if r.AuthedUser.ID == "" || r.Team.ID == "" {
		return "", "", fmt.Errorf("slack oauth: no authenticated user")
	}
	return r.Team.ID, r.AuthedUser.ID, nil
}

// ── hanzo.id OIDC (per-user link, confidential client hanzo-slack) ──────────

// slackTokenSet is the subset of the IAM token response the link flow consumes. A
// refresh token is REQUIRED (offline_access) so an access token can be minted
// later without re-prompting the user.
type slackTokenSet struct {
	Access  string
	Refresh string
	IDToken string
}

// slackOIDCAuthorizeURL builds the hanzo.id authorize redirect for the link flow.
func (s *svc) slackOIDCAuthorizeURL(state string) string {
	q := url.Values{
		"client_id":     {slackIAMClientID()},
		"redirect_uri":  {s.slackLinkCallbackURI()},
		"response_type": {"code"},
		"scope":         {"openid profile email offline_access"},
		"state":         {state},
	}
	return slackIAMBase() + "/oauth/authorize?" + q.Encode()
}

// slackOIDCExchange swaps an authorization code for a token set. hanzo-slack is a
// confidential client (client_secret), so no PKCE.
func (s *svc) slackOIDCExchange(ctx context.Context, code string) (slackTokenSet, error) {
	return slackOIDCToken(ctx, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {s.slackLinkCallbackURI()},
		"client_id":     {slackIAMClientID()},
		"client_secret": {slackIAMClientSecret()},
	})
}

// slackOIDCToken performs an OAuth2 token request. The secrets (code /
// client_secret) are POST-form only and never logged.
func slackOIDCToken(ctx context.Context, form url.Values) (slackTokenSet, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, slackIAMBase()+"/oauth/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return slackTokenSet{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := slackHTTP.Do(req)
	if err != nil {
		return slackTokenSet{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, slackMaxBody))
	if resp.StatusCode != http.StatusOK {
		return slackTokenSet{}, fmt.Errorf("slack: IAM token status %d", resp.StatusCode)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return slackTokenSet{}, fmt.Errorf("slack: IAM token decode: %w", err)
	}
	if out.Error != "" {
		return slackTokenSet{}, fmt.Errorf("slack: IAM token: %s", out.Error)
	}
	if out.AccessToken == "" {
		return slackTokenSet{}, fmt.Errorf("slack: IAM token: empty access_token")
	}
	return slackTokenSet{Access: out.AccessToken, Refresh: out.RefreshToken, IDToken: out.IDToken}, nil
}

// slackOIDCIdentity resolves the linked account's IAM subject (sub) + org (owner)
// from the AUTHENTICATED userinfo endpoint only. The org stored on the link is
// informational (the RUN's org is the workspace org, from OrgForExternalID), so an
// absent owner yields "" rather than trusting an unverified claim.
func (s *svc) slackOIDCIdentity(ctx context.Context, access string) (sub, org string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, slackIAMBase()+"/oauth/userinfo", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	resp, err := slackHTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("slack: IAM userinfo status %d", resp.StatusCode)
	}
	var u struct {
		Sub   string `json:"sub"`
		Owner string `json:"owner"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, slackMaxBody)).Decode(&u); err != nil {
		return "", "", err
	}
	if u.Sub == "" {
		return "", "", fmt.Errorf("slack: IAM userinfo missing sub")
	}
	return u.Sub, u.Owner, nil
}

// ── config (env, read at call time) ─────────────────────────────────────────

// slackLinkConfigured reports whether the full per-user link (Slack sign-in +
// hanzo.id OIDC via the dedicated hanzo-slack client) can run.
func (s *svc) slackLinkConfigured() bool {
	c := slackCreds() // SLACK_CLIENT_ID / SLACK_CLIENT_SECRET
	return c.ClientID != "" && c.ClientSecret != "" &&
		slackIAMClientID() != "" && slackIAMClientSecret() != ""
}

func slackIAMClientID() string { return strings.TrimSpace(os.Getenv("SLACK_LINK_IAM_CLIENT_ID")) }
func slackIAMClientSecret() string {
	return strings.TrimSpace(os.Getenv("SLACK_LINK_IAM_CLIENT_SECRET"))
}

// slackIAMBase resolves the hanzo.id OIDC surface: {IAM_ENDPOINT}/v1/iam
// (default https://hanzo.id).
func slackIAMBase() string {
	ep := strings.TrimSpace(os.Getenv("IAM_ENDPOINT"))
	if ep == "" {
		ep = "https://hanzo.id"
	}
	return strings.TrimRight(ep, "/") + "/v1/iam"
}

// slackLinkCallbackURI is the hanzo.id OIDC redirect (leg3). One env override, else
// derived from the deployment domain — mirroring the OAuth provider's redirectURI.
func (s *svc) slackLinkCallbackURI() string {
	if v := strings.TrimSpace(os.Getenv("SLACK_LINK_REDIRECT_URI")); v != "" {
		return v
	}
	return "https://" + s.domain + "/v1/integrations/slack/link/callback"
}

// slackLinkSlackURI is the Slack sign-in redirect (leg2).
func (s *svc) slackLinkSlackURI() string {
	if v := strings.TrimSpace(os.Getenv("SLACK_LINK_SLACK_REDIRECT_URI")); v != "" {
		return v
	}
	return "https://" + s.domain + "/v1/integrations/slack/link/slack"
}
