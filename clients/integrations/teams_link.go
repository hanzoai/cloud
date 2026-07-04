package integrations

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zap-proto/zip"
)

// teams_link.go is Teams' per-user account link: the PLATFORM-IDENTIFY leg is an
// Azure AD `openid` sign-in in the workspace's tenant (AAD proves which user, via
// the id_token `oid` — the SAME id the inbound activity carries as
// from.aadObjectId), then the shared hanzo.id OIDC leg (bridge_link.go):
//
//	GET /v1/integrations/teams/link           leg1: set __Host init cookie, redirect to AAD sign-in
//	GET /v1/integrations/teams/link/aad        leg2: AAD callback; verify oid+tid, bind (tenant,oid)
//	GET /v1/integrations/teams/link/callback    leg3: hanzo.id OIDC callback; bind Teams↔Hanzo, seal refresh
//
// The bound AAD user comes ONLY from the AAD-signed id_token (leg2), and the leg
// re-checks the token's tenant (tid) equals the chat's tenant — a forwarded link can
// only bind the clicker's own, same-tenant account.

const (
	teamsInitCookie = "__Host-hanzo_tms_init"
	teamsOIDCCookie = "__Host-hanzo_tms_oidc"
)

func teamsLinkConfigured() bool { return teamsConfigured() && oidcConfigured() }

// teamsLinkURL builds the per-user link URL. The tenant id is PROVENANCE (→ org at
// redemption); the AAD user is taken from the sign-in leg, never this URL.
func (s *svc) teamsLinkURL(tenant, _user string) (string, error) {
	if !teamsLinkConfigured() {
		return "", fmt.Errorf("teams: account linking not configured")
	}
	state, err := signSubject(s.stateKey, "tmsentry:"+tenant, 0)
	if err != nil {
		return "", err
	}
	return "https://" + s.domain + "/v1/integrations/teams/link?state=" + url.QueryEscape(state), nil
}

// ── leg 1: redirect to AAD sign-in (in the chat's tenant) ───────────────────

func (s *svc) teamsLink(c *zip.Ctx) error {
	bridgeReady()
	if !teamsLinkConfigured() {
		return zip.Errorf(http.StatusServiceUnavailable, "account linking not configured")
	}
	subject, _, ok := verifySubject(s.stateKey, c.Query("state"), 0)
	if !ok || !strings.HasPrefix(subject, "tmsentry:") {
		return zip.ErrBadRequest("invalid or expired link")
	}
	tenant := strings.TrimPrefix(subject, "tmsentry:")
	nonce, err := genToken()
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	state, err := signSubject(s.stateKey, "tms:"+tenant+":"+nonce, 0)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "state: %v", err)
	}
	setLinkCookie(c, teamsInitCookie, nonce)
	// Sign in against the chat's OWN tenant so only that tenant's users can complete.
	q := url.Values{
		"client_id":     {teamsAppID()},
		"response_type": {"code"},
		"redirect_uri":  {s.teamsLinkAADURI()},
		"response_mode": {"query"},
		"scope":         {"openid profile"},
		"state":         {state},
		"prompt":        {"select_account"},
	}
	return redirect(c, teamsAADBase+"/"+url.PathEscape(tenant)+"/oauth2/v2.0/authorize?"+q.Encode())
}

// ── leg 2: AAD callback (verify the AAD identity) ───────────────────────────

func (s *svc) teamsLinkAAD(c *zip.Ctx) error {
	bridgeReady()
	if !teamsLinkConfigured() {
		return zip.Errorf(http.StatusServiceUnavailable, "account linking not configured")
	}
	if e := strings.TrimSpace(c.Query("error")); e != "" {
		clearLinkCookie(c, teamsInitCookie)
		return zip.ErrBadRequest("microsoft authorization declined")
	}
	code := strings.TrimSpace(c.Query("code"))
	state := c.Query("state")
	if code == "" || state == "" {
		return zip.ErrBadRequest("missing code or state")
	}
	if len(code) > maxCodeLen {
		return zip.ErrBadRequest("authorization code too large")
	}
	subject, _, ok := verifySubject(s.stateKey, state, 0)
	if !ok || !strings.HasPrefix(subject, "tms:") {
		clearLinkCookie(c, teamsInitCookie)
		return zip.ErrBadRequest("invalid or expired link")
	}
	parts := strings.SplitN(strings.TrimPrefix(subject, "tms:"), ":", 2)
	if len(parts) != 2 {
		clearLinkCookie(c, teamsInitCookie)
		return zip.ErrBadRequest("invalid link session")
	}
	tenant, nonce := parts[0], parts[1]
	if init := readLinkCookie(c, teamsInitCookie); init == "" || init != nonce {
		clearLinkCookie(c, teamsInitCookie)
		return zip.ErrBadRequest("link session mismatch; restart from Teams")
	}
	oid, tid, err := teamsExchangeIdentify(c.Context(), tenant, code, s.teamsLinkAADURI())
	if err != nil {
		s.log.Error("teams: aad identify exchange", "err", err)
		clearLinkCookie(c, teamsInitCookie)
		return zip.ErrBadRequest("microsoft sign-in failed")
	}
	// The AAD-verified tenant of the signed-in user MUST equal the chat's tenant.
	if tid != tenant {
		clearLinkCookie(c, teamsInitCookie)
		return zip.ErrBadRequest("sign-in tenant does not match")
	}
	if _, ok := OrgForExternalID("teams", tenant); !ok {
		clearLinkCookie(c, teamsInitCookie)
		return zip.ErrBadRequest("tenant not connected")
	}
	oidcVal, err := signSubject(s.stateKey, "tmslink:"+tenant+":"+oid, 0)
	if err != nil {
		clearLinkCookie(c, teamsInitCookie)
		return zip.Errorf(http.StatusInternalServerError, "link state: %v", err)
	}
	clearLinkCookie(c, teamsInitCookie)
	setLinkCookie(c, teamsOIDCCookie, oidcVal)
	return redirect(c, oidcAuthorizeURL(s.teamsLinkCallbackURI(), oidcVal))
}

// ── leg 3: hanzo.id OIDC callback (bind Teams↔Hanzo) ────────────────────────

func (s *svc) teamsLinkCallback(c *zip.Ctx) error {
	bridgeReady()
	if !teamsLinkConfigured() {
		return zip.Errorf(http.StatusServiceUnavailable, "account linking not configured")
	}
	cookie := readLinkCookie(c, teamsOIDCCookie)
	if e := strings.TrimSpace(c.Query("error")); e != "" {
		clearLinkCookie(c, teamsOIDCCookie)
		return zip.ErrBadRequest("authorization declined")
	}
	code := strings.TrimSpace(c.Query("code"))
	state := c.Query("state")
	if code == "" {
		clearLinkCookie(c, teamsOIDCCookie)
		return zip.ErrBadRequest("missing code")
	}
	if len(code) > maxCodeLen {
		clearLinkCookie(c, teamsOIDCCookie)
		return zip.ErrBadRequest("authorization code too large")
	}
	if cookie == "" {
		return zip.ErrBadRequest("link session missing; restart from Teams")
	}
	if state == "" || state != cookie {
		clearLinkCookie(c, teamsOIDCCookie)
		return zip.ErrBadRequest("link state mismatch")
	}
	subject, nonce, ok := verifySubject(s.stateKey, cookie, 0)
	if !ok || !strings.HasPrefix(subject, "tmslink:") {
		clearLinkCookie(c, teamsOIDCCookie)
		return zip.ErrBadRequest("invalid or expired link")
	}
	parts := strings.SplitN(strings.TrimPrefix(subject, "tmslink:"), ":", 2)
	if len(parts) != 2 {
		clearLinkCookie(c, teamsOIDCCookie)
		return zip.ErrBadRequest("invalid link session")
	}
	tenant, oid := parts[0], parts[1]
	org, iok := OrgForExternalID("teams", tenant)
	if !iok {
		clearLinkCookie(c, teamsOIDCCookie)
		return zip.ErrBadRequest("tenant not connected")
	}
	if !s.kmsReady() {
		clearLinkCookie(c, teamsOIDCCookie)
		return zip.Errorf(http.StatusServiceUnavailable, "secret store unavailable")
	}
	ctx := c.Context()
	ts, err := oidcExchange(ctx, code, s.teamsLinkCallbackURI())
	if err != nil {
		s.log.Error("teams: link code exchange", "tenant", tenant, "err", err)
		clearLinkCookie(c, teamsOIDCCookie)
		return zip.ErrBadRequest("link failed")
	}
	if bridgeSeen.seenAndAdd(nonce, time.Time{}) {
		clearLinkCookie(c, teamsOIDCCookie)
		return zip.ErrBadRequest("link already used")
	}
	if ts.Refresh == "" {
		clearLinkCookie(c, teamsOIDCCookie)
		return zip.Errorf(http.StatusInternalServerError, "link incomplete: no refresh token")
	}
	sub, userOrg, err := oidcIdentity(ctx, ts.Access)
	if err != nil {
		s.log.Error("teams: link identity", "tenant", tenant, "err", err)
		clearLinkCookie(c, teamsOIDCCookie)
		return zip.Errorf(http.StatusInternalServerError, "link identity")
	}
	if err := s.putUserLink(org, "teams", oid, userLink{Subject: sub, Org: userOrg, Refresh: ts.Refresh}); err != nil {
		s.log.Error("teams: user link save", "tenant", tenant, "err", err) // never logs the token
		clearLinkCookie(c, teamsOIDCCookie)
		return zip.Errorf(http.StatusInternalServerError, "token store failed")
	}
	clearLinkCookie(c, teamsOIDCCookie)
	s.log.Info("teams: user linked", "tenant", tenant, "aadUser", oid) // no token
	c.Fiber().Status(http.StatusOK).Type("html")
	return c.Fiber().SendString(bridgeLinkedHTML("Teams"))
}

func (s *svc) teamsLinkAADURI() string {
	return "https://" + s.domain + "/v1/integrations/teams/link/aad"
}

func (s *svc) teamsLinkCallbackURI() string {
	return "https://" + s.domain + "/v1/integrations/teams/link/callback"
}

// teamsExchangeIdentify trades the AAD code for the VERIFIED (oid, tid) from the
// id_token. The token arrives directly from the AAD token endpoint over TLS with our
// client secret, so reading its claims is sound for extracting the non-secret oid/tid.
func teamsExchangeIdentify(ctx context.Context, tenant, code, redirectURI string) (oid, tid string, err error) {
	form := url.Values{
		"client_id":     {teamsAppID()},
		"client_secret": {teamsAppPassword()},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"scope":         {"openid profile"},
	}
	endpoint := teamsAADBase + "/" + url.PathEscape(tenant) + "/oauth2/v2.0/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := bridgeHTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, bridgeMaxBody))
	if resp.StatusCode/100 != 2 {
		return "", "", fmt.Errorf("teams identify token http %d", resp.StatusCode)
	}
	var tok struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := json.Unmarshal(body, &tok); err != nil {
		return "", "", fmt.Errorf("teams identify decode: %w", err)
	}
	if tok.Error != "" || tok.IDToken == "" {
		return "", "", fmt.Errorf("teams identify: %s", nonEmpty(tok.Error, "no id_token"))
	}
	o, t := teamsOIDTIDFromIDToken(tok.IDToken)
	if o == "" {
		return "", "", fmt.Errorf("teams identify: id_token has no oid")
	}
	return o, t, nil
}

// teamsOIDTIDFromIDToken decodes (without re-verifying) the id_token payload for the
// user object id (oid) and tenant id (tid). Safe: direct TLS token-endpoint exchange.
func teamsOIDTIDFromIDToken(idToken string) (oid, tid string) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return "", ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var c struct {
		OID string `json:"oid"`
		TID string `json:"tid"`
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", ""
	}
	return c.OID, c.TID
}
