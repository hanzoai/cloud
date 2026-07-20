package team

// This file is the account API the team SPA speaks to at
// /v1/team/account — JSON-RPC over a single POST, plus the /providers,
// /auth/{provider} and /cookie REST siblings. It is the FULL REWRITE of
// github.com/hanzoai/team-go/pkg/account (account.go + types.go): the Base-DAO
// `workspaces`/`members` collections become the raw-SQLite accountStore, and the
// core.RequestEvent handlers become *zip.Ctx handlers. The IAM OAuth bridge
// (authStart/authCallback/exchangeCode/userinfo/oauthBase) is kept as-is over
// net/http — it is an external hop to hanzo.id.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/team/token"
	"github.com/hanzoai/cloud/types"
)

// authCookie is the cookie the SPA's PUT/DELETE /cookie manage; RPC itself rides
// Authorization: Bearer.
const authCookie = "account-token"

// iamTokenCookie carries the caller's IAM access_token (RS256) to the browser so
// the same-origin /v1/agents proxy can forward it to the cloud gateway. HttpOnly —
// never exposed to page JS.
const iamTokenCookie = "hanzo_iam_token"

// stateCookie binds the OAuth `state` nonce (plus the SPA's navigateUrl) to the
// browser that STARTED the flow. Minted per authStart, verified and cleared by
// authCallback — a callback whose state does not match the cookie is a forged or
// replayed flow and is bounced, never exchanged.
const stateCookie = "team-oauth-state"

// stateTTL bounds one OAuth round trip browser→IAM→callback.
const stateTTL = 10 * time.Minute

// Token lifetimes. Every minted token now carries an `exp` (Decode enforces it),
// bounding the replay window on a captured token. The session token matches the
// 30-day cookie; the workspace token — which rides in the transactor URL path and
// is thus log-prone — is short (12h) and re-minted by selectWorkspace on demand.
const (
	sessionTokenTTL   = 30 * 24 * time.Hour
	workspaceTokenTTL = 12 * time.Hour
)

// expUnix returns the unix-second expiry `d` from now — the `exp` claim value.
func expUnix(d time.Duration) int64 { return time.Now().Add(d).Unix() }

type config struct {
	iamEndpoint     string // OIDC issuer base (deps.IAMIssuer / IAM_ENDPOINT)
	iamClientID     string // IAM_CLIENT_ID (KMS-synced env)
	iamClientSecret string // IAM_CLIENT_SECRET (KMS-synced env)
	serverSecret    string // SERVER_SECRET (KMS-synced env) — HS256 signing key
	frontURL        string // browser destination after IAM (default: request origin)
	transactor      string // wss:// base returned by selectWorkspace (default: derived)
	provider        string // IAM provider name surfaced to the SPA ("openid")
	// publicURL is the deployment's PUBLIC origin (e.g. https://hanzo.team), from
	// TEAM_PUBLIC_URL / PUBLIC_ORIGIN. Behind the gateway the request Host is the
	// INTERNAL cluster host (cloud.hanzo.svc:8000), which IAM rejects as an OAuth
	// callback — so when set this is the origin the redirect_uri (and the front
	// bounce) are built from, letting cloud sit behind the gateway uniformly. Unset
	// → the request origin (originOf), unchanged for the direct-route deployment.
	publicURL string
}

// api is the account control-plane handler set.
type api struct {
	accounts *accountStore
	trans    *transServer
	cfg      config
	log      luxlog.Logger
	// verify is cloud's RS256/JWKS IAM token validator (cloud.NewTokenValidator —
	// the SAME trust anchor as the identity boundary). The OAuth callback derives
	// the tenant from ITS verdict, never from unverified claims.
	verify func(string) (cloud.VerifiedIdentity, error)
	// commerce answers CheckEntitlement(org, "team") at workspace select — nil
	// (not co-resident) is an infra absence and never blocks login.
	commerce types.CommerceClient
	// planEnt resolves a plan id to its entitlement block (plan.Entitlements) —
	// the source of the team.guests cap.
	planEnt func(context.Context, string) (map[string]any, error)
}

// ── types (ported from team-go/pkg/account/types.go) ──────────────────────────

// Role is the platform AccountRole. Stored on members.role (lowercased) and
// surfaced uppercased in WorkspaceLoginInfo.role.
type Role = string

type rpcRequest struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

// LoginInfo is the base login response. token overrides any prior token.
type LoginInfo struct {
	Account  string `json:"account"`
	Name     string `json:"name,omitempty"`
	SocialID string `json:"socialId,omitempty"`
	Token    string `json:"token,omitempty"`
}

// WorkspaceLoginInfo extends LoginInfo — returned by selectWorkspace. token is the
// per-workspace JWT; endpoint is the transactor wss:// base the client connects to.
type WorkspaceLoginInfo struct {
	LoginInfo
	Workspace        string `json:"workspace"`
	WorkspaceDataID  string `json:"workspaceDataId,omitempty"`
	WorkspaceURL     string `json:"workspaceUrl"`
	Endpoint         string `json:"endpoint"`
	Role             Role   `json:"role"`
	AllowGuestSignUp bool   `json:"allowGuestSignUp,omitempty"`
}

// WorkspaceInfo is one entry of getUserWorkspaces. The version triple is the Team
// MODEL version (the SAME source the transactor reports as serverVersion).
type WorkspaceInfo struct {
	UUID         string `json:"uuid"`
	Name         string `json:"name"`
	URL          string `json:"url"`
	DataID       string `json:"dataId,omitempty"`
	Region       string `json:"region"`
	Mode         string `json:"mode"`
	VersionMajor int    `json:"versionMajor"`
	VersionMinor int    `json:"versionMinor"`
	VersionPatch int    `json:"versionPatch"`
	LastVisit    int64  `json:"lastVisit,omitempty"`
	IsDisabled   bool   `json:"isDisabled"`
	CreatedOn    int64  `json:"createdOn,omitempty"`
}

// ProviderInfo is one entry of GET /providers.
type ProviderInfo struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
}

// RegionInfo is one entry of getRegionInfo.
type RegionInfo struct {
	Region string `json:"region"`
	Name   string `json:"name"`
}

// SocialID is one entry of getSocialIds. The workbench connect flow runs
// pickPrimarySocialId over these: it needs at least one non-deleted id and prefers
// type "hanzo".
type SocialID struct {
	ID           string `json:"_id"`
	Type         string `json:"type"`
	Value        string `json:"value"`
	Key          string `json:"key"`
	DisplayValue string `json:"displayValue,omitempty"`
	VerifiedOn   int64  `json:"verifiedOn,omitempty"`
	IsDeleted    bool   `json:"isDeleted,omitempty"`
}

// Status is the platform PlatformError payload sent as {"error": Status}.
type Status struct {
	Severity int            `json:"severity"`
	Code     string         `json:"code"`
	Params   map[string]any `json:"params"`
}

func statusUnauthorized(msg string) Status {
	return Status{Severity: 1, Code: "account:status:Unauthorized", Params: map[string]any{"message": msg}}
}
func statusError(msg string) Status {
	return Status{Severity: 1, Code: "account:status:InternalServerError", Params: map[string]any{"message": msg}}
}
func statusWorkspaceNotFound(url string) Status {
	return Status{Severity: 1, Code: "account:status:WorkspaceNotFound", Params: map[string]any{"workspace": url}}
}

// ── route registration ────────────────────────────────────────────────────────

func (g *api) register(r zip.Router, guard guardFn) {
	r.Post("/account", guard(g.rpc))
	r.Get("/account/providers", guard(g.providers))
	r.Get("/account/auth/:provider", guard(g.authStart))
	r.Get("/account/auth/:provider/callback", guard(g.authCallback))
	r.Put("/account/cookie", guard(g.setCookie))
	r.Delete("/account/cookie", guard(g.clearCookie))
}

// ── REST: providers ───────────────────────────────────────────────────────────

func (g *api) providers(c *zip.Ctx) error {
	// Only IAM. The full method set (email/SMS/Google/GitHub/Web3) is presented by
	// IAM itself once the browser reaches /auth/openid.
	return c.JSON(http.StatusOK, []ProviderInfo{{Name: g.cfg.provider, DisplayName: "Hanzo"}})
}

// ── REST: IAM OAuth bridge (external hop, net/http) ────────────────────────────

// authStart redirects the browser into IAM's authorize endpoint. team is a
// confidential client (client_secret), so no PKCE — the code is exchanged
// server-side in authCallback. state is a RANDOM nonce bound to a short-lived
// cookie (never the bare navigateUrl): the callback only proceeds when the two
// match, so a cross-site-initiated or replayed callback is refused.
func (g *api) authStart(c *zip.Ctx) error {
	provider := providerParam(c)
	origin := g.callbackOrigin(c)
	redirect := origin + "/v1/team/account/auth/" + provider + "/callback"
	nonce, err := randState()
	if err != nil {
		return c.String(http.StatusInternalServerError, "state")
	}
	// The navigateUrl rides IN the cookie (escaped) next to the nonce, so the
	// round trip needs no second channel and the value stays server-bound.
	g.setSessionCookie(c, stateCookie, nonce+"|"+url.QueryEscape(c.Query("navigateUrl")), int(stateTTL.Seconds()))
	q := url.Values{
		"client_id":     {g.cfg.iamClientID},
		"redirect_uri":  {redirect},
		"response_type": {"code"},
		"scope":         {"openid profile email"},
		"state":         {nonce},
	}
	return c.Fiber().Redirect().Status(http.StatusFound).To(oauthBase(g.cfg.iamEndpoint) + "/oauth/authorize?" + q.Encode())
}

// authCallback verifies the state nonce against the flow cookie, exchanges the
// IAM code for the user, ensures the account has a workspace, mints the account
// token, and bounces the browser back to the SPA with ?token= (which Auth reads
// via getLoginInfoFromQuery).
func (g *api) authCallback(c *zip.Ctx) error {
	provider := providerParam(c)
	// One-shot state: read + clear the flow cookie FIRST, then require the
	// callback's state to match the nonce it holds — before any error/code
	// handling, so a forged callback never reaches the exchange.
	nonce, navigate := g.stateFromCookie(c)
	g.setSessionCookie(c, stateCookie, "", -1)
	if nonce == "" || c.Query("state") != nonce {
		return g.bounce(c, "", "", "state_mismatch")
	}
	if e := c.Query("error"); e != "" {
		return g.bounce(c, "", navigate, e)
	}
	code := c.Query("code")
	if code == "" {
		return g.bounce(c, "", navigate, "missing_code")
	}
	origin := g.callbackOrigin(c)
	redirect := origin + "/v1/team/account/auth/" + provider + "/callback"

	access, err := g.exchangeCode(code, redirect)
	if err != nil {
		g.log.Error("account: oauth code exchange", "err", err)
		return g.bounce(c, "", navigate, "exchange_failed")
	}
	sub, email, name, err := g.userinfo(access)
	if err != nil {
		return g.bounce(c, "", navigate, "userinfo_failed")
	}
	// AccountUuid = the IAM sub (derived to a stable UUID when the sub is not one).
	account := accountID(sub)
	// Tenant = the IAM org — the access token's `owner` claim, accepted ONLY off a
	// VERIFIED token (RS256 against the IAM JWKS, same trust anchor as the identity
	// boundary). It scopes every workspace + data file — full multitenancy — so a
	// verification failure fails CLOSED: no fallback org, no login.
	id, err := g.verify(access)
	if err != nil || id.Owner == "" {
		g.log.Error("account: iam token verify", "err", err)
		return g.bounce(c, "", navigate, "org_failed")
	}
	org := id.Owner
	ctx := c.Context()
	displayName := firstNonEmpty(name, localPart(email))
	if _, err := g.accounts.EnsureWorkspace(ctx, org, account, displayName); err != nil {
		g.log.Error("account: ensure workspace", "err", err)
	}
	// Fill the human display name so the roster reconcile renders a name, not the
	// account uuid. Idempotent (only fills empty).
	_ = g.accounts.EnsureMemberName(ctx, account, displayName)

	tok, err := token.Generate(account, "", map[string]any{"org": org}, expUnix(sessionTokenTTL), g.cfg.serverSecret)
	if err != nil {
		return g.bounce(c, "", navigate, "token_failed")
	}
	// Retain the IAM access_token (RS256) in an HttpOnly cookie so the same-origin
	// agents proxy can forward it to the cloud gateway. Page JS never reads it.
	g.setIAMTokenCookie(c, access)
	return g.bounce(c, tok, navigate, "")
}

// stateFromCookie splits the flow cookie into its nonce and the escaped
// navigateUrl it carries. Empty nonce ⇒ no live flow.
func (g *api) stateFromCookie(c *zip.Ctx) (nonce, navigate string) {
	raw := c.Fiber().Req().Cookies(stateCookie)
	nonce, esc, _ := strings.Cut(raw, "|")
	navigate, _ = url.QueryUnescape(esc)
	return nonce, navigate
}

// randState mints the OAuth state nonce: 128 bits of crypto/rand, hex.
func randState() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// ── REST: cookie ──────────────────────────────────────────────────────────────

func (g *api) setCookie(c *zip.Ctx) error {
	var body struct {
		Token string `json:"token"`
	}
	_ = c.Bind(&body)
	if body.Token == "" {
		body.Token = bearer(c)
	}
	// VERIFY the token (signature + expiry) before persisting it as the session
	// cookie. Storing an unverified, caller-supplied value is a login-CSRF /
	// session-fixation vector — an attacker could pin a cookie the victim's browser
	// then presents as authenticated. Only a token THIS service signed is accepted.
	if _, err := token.Decode(body.Token, g.cfg.serverSecret, true); err != nil {
		return zip.ErrUnauthorized("invalid session token")
	}
	g.setSessionCookie(c, authCookie, body.Token, int(sessionTokenTTL.Seconds()))
	return c.JSON(http.StatusOK, map[string]any{"result": true})
}

func (g *api) clearCookie(c *zip.Ctx) error {
	g.setSessionCookie(c, authCookie, "", -1)
	return c.JSON(http.StatusOK, map[string]any{"result": true})
}

// ── JSON-RPC ──────────────────────────────────────────────────────────────────

func (g *api) rpc(c *zip.Ctx) error {
	var req rpcRequest
	if err := c.Bind(&req); err != nil {
		return g.fail(c, statusError("bad request"))
	}
	switch req.Method {
	case "getLoginInfoByToken", "getLoginWithWorkspaceInfo":
		return g.getLoginInfoByToken(c)
	case "getUserWorkspaces":
		return g.getUserWorkspaces(c)
	case "selectWorkspace":
		return g.selectWorkspace(c, req.Params)
	case "getWorkspaceInfo":
		return g.getWorkspaceInfo(c)
	case "getRegionInfo":
		return g.ok(c, []RegionInfo{{Region: "", Name: "Default"}})
	case "getSocialIds":
		return g.getSocialIds(c)
	case "getPerson":
		return g.getPerson(c)
	case "isReadOnlyGuest":
		return g.ok(c, false)
	case "loginAsGuest":
		return g.fail(c, statusUnauthorized("guest login disabled"))
	default:
		return g.fail(c, Status{Severity: 1, Code: "account:status:UnknownMethod", Params: map[string]any{"method": req.Method}})
	}
}

func (g *api) getLoginInfoByToken(c *zip.Ctx) error {
	account, _, tok, err := g.account(c)
	if err != nil {
		return g.fail(c, statusUnauthorized(err.Error()))
	}
	return g.ok(c, LoginInfo{Account: account, Token: tok})
}

func (g *api) getUserWorkspaces(c *zip.Ctx) error {
	account, org, _, err := g.account(c)
	if err != nil {
		return g.fail(c, statusUnauthorized(err.Error()))
	}
	wss, err := g.accounts.WorkspacesOf(c.Context(), org, account)
	if err != nil {
		return g.fail(c, statusError(err.Error()))
	}
	out := []WorkspaceInfo{}
	for _, ws := range wss {
		out = append(out, toWorkspaceInfo(ws))
	}
	return g.ok(c, out)
}

// selectWorkspace resolves the workspace scoped to the caller's VERIFIED token org
// (a foreign tenant's slug is unresolvable), checks membership from the members
// row, then mints the workspace token carrying extra.org and returns the
// transactor wss endpoint under /v1/team/transactor.
func (g *api) selectWorkspace(c *zip.Ctx, params map[string]any) error {
	account, org, _, err := g.account(c)
	if err != nil {
		return g.fail(c, statusUnauthorized(err.Error()))
	}
	wsURL, _ := params["workspaceUrl"].(string)
	ws, err := g.accounts.WorkspaceBySlug(c.Context(), org, wsURL)
	if err != nil {
		return g.fail(c, statusWorkspaceNotFound(wsURL))
	}
	role, ok := g.accounts.Membership(c.Context(), ws.ID, account)
	if !ok {
		return g.fail(c, statusUnauthorized("not a member of "+wsURL))
	}
	// The billing gate: the org's plan must license the team product. 402 carries
	// the upgrade destination; infra errors NEVER block login (see entitle).
	if st := g.entitle(c.Context(), org, role, ws.ID, account); st != nil {
		return c.JSON(http.StatusPaymentRequired, map[string]any{"error": *st, "upgradeUrl": upgradeURL})
	}
	// Carry the tenant into the workspace token so the transactor routes to
	// orgs/<org>/ws/<workspace>.db. Short-lived (workspaceTokenTTL) — it rides in
	// the transactor URL path, so a bounded lifetime caps replay on capture.
	wsTok, err := token.Generate(account, ws.UUID, map[string]any{"org": org}, expUnix(workspaceTokenTTL), g.cfg.serverSecret)
	if err != nil {
		return g.fail(c, statusError("mint workspace token: "+err.Error()))
	}
	return g.ok(c, WorkspaceLoginInfo{
		LoginInfo:       LoginInfo{Account: account, Token: wsTok},
		Workspace:       ws.UUID,
		WorkspaceURL:    ws.Slug,
		WorkspaceDataID: ws.DataID,
		Endpoint:        g.endpoint(c),
		Role:            strings.ToUpper(role),
	})
}

func (g *api) getWorkspaceInfo(c *zip.Ctx) error {
	account, org, _, err := g.account(c)
	if err != nil {
		return g.fail(c, statusUnauthorized(err.Error()))
	}
	wss, err := g.accounts.WorkspacesOf(c.Context(), org, account)
	if err != nil || len(wss) == 0 {
		return g.fail(c, statusWorkspaceNotFound(""))
	}
	return g.ok(c, toWorkspaceInfo(wss[0]))
}

func (g *api) getPerson(c *zip.Ctx) error {
	account, _, _, err := g.account(c)
	if err != nil {
		return g.fail(c, statusUnauthorized(err.Error()))
	}
	return g.ok(c, map[string]any{"uuid": account})
}

// getSocialIds returns the account's single HANZO social identity — the workbench
// connect flow runs pickPrimarySocialId over it (throws on an empty list).
func (g *api) getSocialIds(c *zip.Ctx) error {
	account, _, _, err := g.account(c)
	if err != nil {
		return g.fail(c, statusUnauthorized(err.Error()))
	}
	key := "hanzo:" + account
	return g.ok(c, []SocialID{{
		ID: key, Type: "hanzo", Value: account, Key: key, VerifiedOn: time.Now().UnixMilli(),
	}})
}

// ── helpers ───────────────────────────────────────────────────────────────────

// sessionToken decodes AND verifies (signature + expiry) the request's HS256
// bearer/cookie token — the one this service minted. bearer takes precedence over
// the cookie. It is the ONE place a team session token is turned into a principal,
// shared by the account RPC and the files plane.
//
// The SPA sends OUR HS256 token, not an IAM RS256 JWT: an IAM bearer would simply
// fail the HMAC check (ErrSignature) and be rejected here, so there is no separate
// algorithm routing to maintain (why token.Alg was removed). token.Decode with
// verify=true also enforces `exp`/`nbf`, so a captured expired token is refused.
func sessionToken(c *zip.Ctx, secret string) (*token.Token, string, error) {
	raw := bearer(c)
	if raw == "" {
		raw = c.Fiber().Req().Cookies(authCookie)
	}
	if raw == "" {
		return nil, "", fmt.Errorf("no token")
	}
	t, err := token.Decode(raw, secret, true)
	if err != nil {
		return nil, "", err
	}
	if t.Account == "" {
		return nil, "", fmt.Errorf("token has no account")
	}
	return t, raw, nil
}

// account resolves (AccountUuid, org, token) from the request's verified session
// token. The org is the token's SIGNED extra.org claim — the tenant key for every
// account-store query — never a client header.
func (g *api) account(c *zip.Ctx) (account, org, tok string, err error) {
	t, raw, err := sessionToken(c, g.cfg.serverSecret)
	if err != nil {
		return "", "", "", err
	}
	org, _ = t.Extra["org"].(string)
	return t.Account, org, raw, nil
}

// callbackOrigin is the ORIGIN the OAuth redirect_uri is built from — the SAME
// value in authStart (authorize) and authCallback (token exchange), so the two
// redirect_uri strings are byte-identical (IAM requires the exchange redirect_uri
// to match the authorize one). Behind the gateway the request Host is the internal
// cluster host, which IAM rejects, so a configured public origin (publicURL) wins;
// unset → the request origin (originOf), so the direct-route and every other
// deployment are unchanged. originOf itself is NOT modified (still the fallback and
// used elsewhere).
func (g *api) callbackOrigin(c *zip.Ctx) string {
	if g.cfg.publicURL != "" {
		return g.cfg.publicURL
	}
	return originOf(c)
}

// bounce redirects to the SPA with the minted token (or an error).
func (g *api) bounce(c *zip.Ctx, tok, navigateURL, errCode string) error {
	front := g.cfg.frontURL
	if front == "" {
		// Behind the gateway the request origin is the internal host, so honor the
		// public origin for the front bounce too (else the browser would be sent to
		// cloud.hanzo.svc). FRONT_URL still overrides when set.
		front = g.callbackOrigin(c)
	}
	path := "/login:component:LoginApp/auth"
	if tok == "" {
		path = "/login"
	}
	dest, err := url.Parse(front + path)
	if err != nil {
		return c.String(http.StatusInternalServerError, "bad front url")
	}
	q := dest.Query()
	if tok != "" {
		q.Set("token", tok)
	}
	if errCode != "" {
		q.Set("error", errCode)
	}
	if navigateURL != "" {
		q.Set("navigateUrl", navigateURL)
	}
	dest.RawQuery = q.Encode()
	return c.Fiber().Redirect().Status(http.StatusFound).To(dest.String())
}

func (g *api) ok(c *zip.Ctx, value any) error {
	return c.JSON(http.StatusOK, map[string]any{"result": value})
}

func (g *api) fail(c *zip.Ctx, s Status) error {
	return c.JSON(http.StatusOK, map[string]any{"error": s})
}

// endpoint is the transactor wss:// base selectWorkspace hands back. It is ALWAYS
// namespaced under /v1/team/transactor (never bare /transactor). TRANSACTOR_URL
// overrides the derived value for split deployments.
func (g *api) endpoint(c *zip.Ctx) string {
	if g.cfg.transactor != "" {
		return g.cfg.transactor
	}
	return "wss://" + hostOf(c) + "/v1/team/transactor"
}

// setIAMTokenCookie stores the IAM access_token, expiring the cookie with the
// token itself (from its `exp` claim; falls back to 8h).
func (g *api) setIAMTokenCookie(c *zip.Ctx, access string) {
	maxAge := 8 * 3600
	if secs := secondsUntilExp(access); secs > 0 {
		maxAge = secs
	}
	g.setSessionCookie(c, iamTokenCookie, access, maxAge)
}

// setSessionCookie writes an HttpOnly, Secure, SameSite=Lax cookie. maxAge<0
// clears it.
func (g *api) setSessionCookie(c *zip.Ctx, name, value string, maxAge int) {
	c.Fiber().Res().Cookie(&fiber.Cookie{
		Name: name, Value: value, Path: "/",
		HTTPOnly: true, Secure: true, SameSite: fiber.CookieSameSiteLaxMode,
		MaxAge: maxAge,
	})
}

// ── OAuth net/http helpers (external hop to IAM) ───────────────────────────────

// oauthBase returns the canonical IAM OAuth base URL: ${IAMEndpoint}/v1/iam.
// Hanzo IAM and the embedded provider both mount their OIDC surface
// (authorize/token/userinfo) under /v1/iam — never at the root. One place owns the
// prefix.
func oauthBase(endpoint string) string {
	endpoint = strings.TrimRight(endpoint, "/")
	if endpoint == "" {
		endpoint = "https://hanzo.id"
	}
	return endpoint + "/v1/iam"
}

// exchangeCode exchanges an authorization code for an access token at the
// canonical IAM token endpoint (${IAMEndpoint}/v1/iam/oauth/token). team is a
// confidential client (client_secret), so no PKCE — the code is exchanged
// server-side here.
func (g *api) exchangeCode(code, redirectURI string) (string, error) {
	data := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {g.cfg.iamClientID},
		"client_secret": {g.cfg.iamClientSecret},
	}
	resp, err := http.PostForm(oauthBase(g.cfg.iamEndpoint)+"/oauth/token", data)
	if err != nil {
		return "", fmt.Errorf("token exchange request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("token exchange status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("token exchange decode: %w", err)
	}
	if out.Error != "" {
		return "", fmt.Errorf("token exchange: %s: %s", out.Error, out.ErrorDesc)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("token exchange: empty access_token")
	}
	return out.AccessToken, nil
}

// userinfo fetches the OIDC userinfo for an access token and returns the canonical
// sub/email/name claims.
func (g *api) userinfo(access string) (sub, email, name string, err error) {
	req, err := http.NewRequest(http.MethodGet, oauthBase(g.cfg.iamEndpoint)+"/oauth/userinfo", nil)
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf("userinfo: status %d", resp.StatusCode)
	}
	var u struct {
		Sub   string `json:"sub"`
		Email string `json:"email"`
		Name  string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return "", "", "", err
	}
	if u.Sub == "" {
		return "", "", "", fmt.Errorf("userinfo: missing sub")
	}
	return u.Sub, u.Email, u.Name, nil
}

// secondsUntilExp reads a JWT's `exp` and returns the remaining lifetime in
// seconds, or 0 if absent/expired/unparseable.
func secondsUntilExp(jwtTok string) int {
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if decodeJWTClaims(jwtTok, &claims) != nil || claims.Exp == 0 {
		return 0
	}
	d := time.Until(time.Unix(claims.Exp, 0))
	if d <= 0 {
		return 0
	}
	return int(d.Seconds())
}

// decodeJWTClaims base64url-decodes a JWT payload segment into v without verifying
// the signature. Used only for reading claims off a token we already trust.
func decodeJWTClaims(jwtTok string, v any) error {
	parts := strings.Split(jwtTok, ".")
	if len(parts) < 2 {
		return fmt.Errorf("not a jwt")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		if raw, err = base64.StdEncoding.DecodeString(parts[1]); err != nil {
			return err
		}
	}
	return json.Unmarshal(raw, v)
}

// ── request/identity helpers ──────────────────────────────────────────────────

// accountID is the ONE way to derive team's canonical account id from an IAM
// subject — ported VERBATIM from team-go/pkg/wsauth.AccountID. A sub that is
// already a UUID is used verbatim; any other sub maps to a stable UUIDv5
// (namespace "iam:<sub>"). Empty in → empty out.
func accountID(sub string) string {
	sub = strings.TrimSpace(sub)
	if sub == "" {
		return ""
	}
	if uuid.Validate(sub) != nil {
		return uuid.NewSHA1(uuid.NameSpaceURL, []byte("iam:"+sub)).String()
	}
	return sub
}

func providerParam(c *zip.Ctx) string {
	p := strings.TrimSpace(c.Param("provider"))
	if p == "" {
		return "openid"
	}
	return p
}

// toWorkspaceInfo flattens a workspace for getUserWorkspaces. The version triple
// is the MODEL version (the SAME source the transactor reports as
// serverVersion) so the workspace-model version and the server version never drift.
func toWorkspaceInfo(ws workspace) WorkspaceInfo {
	return WorkspaceInfo{
		UUID: ws.UUID, Name: ws.Name, URL: ws.Slug, DataID: ws.DataID, Region: ws.Region,
		Mode:         "active",
		VersionMajor: modelMajor(), VersionMinor: modelMinor(), VersionPatch: modelPatch(),
	}
}

func bearer(c *zip.Ctx) string { return bearerFromHeader(c.Header("Authorization")) }

// bearerFromHeader extracts the token from an "Authorization: Bearer <t>" header
// (scheme case-insensitive). Empty when absent or not a bearer.
func bearerFromHeader(h string) string {
	if len(h) > 7 && strings.EqualFold(h[:7], "Bearer ") {
		return h[7:]
	}
	return ""
}

// hostOf returns the request host (X-Forwarded-Host aware via fiber's Host()).
func hostOf(c *zip.Ctx) string {
	if h := strings.TrimSpace(c.Fiber().Host()); h != "" {
		return h
	}
	return "localhost"
}

// originOf returns the request's scheme://host. Scheme is https unless the request
// is plainly local, matching team-go's originOf.
func originOf(c *zip.Ctx) string {
	host := hostOf(c)
	scheme := "https"
	if proto := c.Header("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1") {
		scheme = "http"
	}
	return scheme + "://" + host
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func localPart(email string) string {
	if i := strings.Index(email, "@"); i > 0 {
		return email[:i]
	}
	return email
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "ws"
	}
	return out
}

func shortID() string {
	return strings.Split(uuid.NewString(), "-")[0]
}
