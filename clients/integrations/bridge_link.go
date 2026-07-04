package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/zap-proto/zip"
)

// bridge_link.go is the SHARED hanzo.id OIDC leg of the account-link flow, reused
// by every non-Slack chat adapter (Discord/Teams/Telegram). Each adapter supplies
// its own PLATFORM-IDENTIFY leg (Discord OAuth `identify`, Teams AAD, Telegram
// Login Widget) that proves WHICH platform user is linking; this file is the
// second leg they all share — authenticate the Hanzo account at hanzo.id, mint a
// refresh token, resolve the subject — so the OIDC round trip is written ONCE.
//
// ONE confidential hanzo.id client ("hanzo-chat") backs all of them
// (BRIDGE_LINK_IAM_CLIENT_ID/SECRET) with each platform's link-callback registered
// as a redirect URI. (Slack keeps its own SLACK_LINK_IAM_* client for continuity;
// converging it onto this helper is a follow-up.)

// bridgeHTTP bounds every bridge-side call: a real timeout (fail-secure — never an
// infinite hang) and no redirect-following surprises. Shared by the new adapters'
// platform + OIDC HTTP.
var bridgeHTTP = &http.Client{Timeout: 15 * time.Second}

// ── shared __Host- link cookies (browser-binding for every adapter's link) ──
//
// A __Host- cookie is host-bound (Secure + no Domain + Path=/), so a sibling
// *.hanzo.ai origin cannot plant/fixate a same-named cookie. SameSite=Lax so it
// survives the top-level GET redirect back from the platform / hanzo.id. Every
// account-link flow (Discord/Teams/Telegram + Slack, once converged) uses these.

func setLinkCookie(c *zip.Ctx, name, val string) {
	c.Fiber().Cookie(&fiber.Cookie{
		Name: name, Value: val, Path: "/",
		MaxAge: linkStateTTLSec, HTTPOnly: true, Secure: true,
		SameSite: fiber.CookieSameSiteLaxMode,
	})
}

func readLinkCookie(c *zip.Ctx, name string) string { return c.Fiber().Cookies(name) }

// bridgeLinkedHTML is the terse success page shown after a user links their account
// from any platform. platform is the display name ("Discord","Telegram","Teams").
func bridgeLinkedHTML(platform string) string {
	return `<!doctype html><meta charset="utf-8"><title>Hanzo connected</title>` +
		`<body style="font-family:system-ui,sans-serif;max-width:32rem;margin:4rem auto;text-align:center">` +
		`<h1>Hanzo connected</h1><p>Your Hanzo account is linked. Return to ` + platform +
		` and mention <b>@hanzo</b>.</p></body>`
}

func clearLinkCookie(c *zip.Ctx, name string) {
	c.Fiber().Cookie(&fiber.Cookie{
		Name: name, Value: "", Path: "/",
		MaxAge: -1, HTTPOnly: true, Secure: true,
		SameSite: fiber.CookieSameSiteLaxMode,
	})
}

// bridgeTokenSet is the subset of the IAM token response the link flow consumes. A
// refresh token is REQUIRED (offline_access) so an access token can be minted later
// without re-prompting the user.
type bridgeTokenSet struct {
	Access  string
	Refresh string
	IDToken string
}

// oidcBase resolves the hanzo.id OIDC surface: {IAM_ENDPOINT}/v1/iam (default
// https://hanzo.id). Shared with the Slack link (same IAM host).
func oidcBase() string {
	ep := strings.TrimSpace(os.Getenv("IAM_ENDPOINT"))
	if ep == "" {
		ep = "https://hanzo.id"
	}
	return strings.TrimRight(ep, "/") + "/v1/iam"
}

func linkIAMClientID() string { return strings.TrimSpace(os.Getenv("BRIDGE_LINK_IAM_CLIENT_ID")) }
func linkIAMClientSecret() string {
	return strings.TrimSpace(os.Getenv("BRIDGE_LINK_IAM_CLIENT_SECRET"))
}

// oidcConfigured reports whether the shared hanzo.id confidential client is present.
func oidcConfigured() bool { return linkIAMClientID() != "" && linkIAMClientSecret() != "" }

// oidcAuthorizeURL builds the hanzo.id authorize redirect for a link flow. state
// ties the OIDC leg to the browser (the adapter's leg checks state == its cookie).
func oidcAuthorizeURL(redirectURI, state string) string {
	q := url.Values{
		"client_id":     {linkIAMClientID()},
		"redirect_uri":  {redirectURI},
		"response_type": {"code"},
		"scope":         {"openid profile email offline_access"},
		"state":         {state},
	}
	return oidcBase() + "/oauth/authorize?" + q.Encode()
}

// oidcExchange swaps an authorization code for a token set (confidential client, no
// PKCE). The secrets (code / client_secret) are POST-form only and never logged.
func oidcExchange(ctx context.Context, code, redirectURI string) (bridgeTokenSet, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {linkIAMClientID()},
		"client_secret": {linkIAMClientSecret()},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oidcBase()+"/oauth/token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return bridgeTokenSet{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := bridgeHTTP.Do(req)
	if err != nil {
		return bridgeTokenSet{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, bridgeMaxBody))
	if resp.StatusCode != http.StatusOK {
		return bridgeTokenSet{}, fmt.Errorf("bridge: IAM token status %d", resp.StatusCode)
	}
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		IDToken      string `json:"id_token"`
		Error        string `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return bridgeTokenSet{}, fmt.Errorf("bridge: IAM token decode: %w", err)
	}
	if out.Error != "" {
		return bridgeTokenSet{}, fmt.Errorf("bridge: IAM token: %s", out.Error)
	}
	if out.AccessToken == "" {
		return bridgeTokenSet{}, fmt.Errorf("bridge: IAM token: empty access_token")
	}
	return bridgeTokenSet{Access: out.AccessToken, Refresh: out.RefreshToken, IDToken: out.IDToken}, nil
}

// oidcIdentity resolves the linked account's IAM subject (sub) + org (owner) from
// the AUTHENTICATED userinfo endpoint only. The org stored on the link is
// informational (the RUN's org is the workspace org, from OrgForExternalID), so an
// absent owner yields "" rather than trusting an unverified claim.
func oidcIdentity(ctx context.Context, access string) (sub, org string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, oidcBase()+"/oauth/userinfo", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	resp, err := bridgeHTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("bridge: IAM userinfo status %d", resp.StatusCode)
	}
	var u struct {
		Sub   string `json:"sub"`
		Owner string `json:"owner"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, bridgeMaxBody)).Decode(&u); err != nil {
		return "", "", err
	}
	if u.Sub == "" {
		return "", "", fmt.Errorf("bridge: IAM userinfo missing sub")
	}
	return u.Sub, u.Owner, nil
}
