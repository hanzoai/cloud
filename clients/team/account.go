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
	"bytes"
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
	"github.com/hanzoai/cloud/clients/agents"
	"github.com/hanzoai/cloud/clients/team/token"
	"github.com/hanzoai/cloud/types"
	"github.com/hanzoai/iam-v1"
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
	UUID   string `json:"uuid"`
	Name   string `json:"name"`
	URL    string `json:"url"`
	DataID string `json:"dataId,omitempty"`
	// Org is the workspace's owning IAM tenant. getUserWorkspaces unions a user's
	// workspaces across every org they belong to, so the client switcher groups by
	// this field (a user in two orgs sees both orgs' workspaces, each tagged).
	Org          string `json:"org,omitempty"`
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
// Severity is the platform's STRING enum ("OK"/"INFO"/"WARNING"/"ERROR") — the
// SPA compares it against those literals, so a numeric severity matches nothing.
type Status struct {
	Severity string         `json:"severity"`
	Code     string         `json:"code"`
	Params   map[string]any `json:"params"`
}

func statusUnauthorized(msg string) Status {
	return Status{Severity: "ERROR", Code: "account:status:Unauthorized", Params: map[string]any{"message": msg}}
}
func statusError(msg string) Status {
	return Status{Severity: "ERROR", Code: "account:status:InternalServerError", Params: map[string]any{"message": msg}}
}
func statusWorkspaceNotFound(url string) Status {
	return Status{Severity: "ERROR", Code: "account:status:WorkspaceNotFound", Params: map[string]any{"workspace": url}}
}

// statusBadRequest is the clean 400-shaped refusal for a missing required RPC
// param (e.g. selectWorkspace with no workspaceUrl). Delivered in the RPC error
// envelope (HTTP 200 body carrying {error: Status}, the established account-RPC
// convention the SPA translates) — never a silent success or a default.
func statusBadRequest(msg string) Status {
	return Status{Severity: "ERROR", Code: "account:status:BadRequest", Params: map[string]any{"message": msg}}
}

// statusAmbiguous is the refusal when an explicit workspace slug resolves in more
// than one of the caller's orgs: the caller must disambiguate, so the server
// returns a clean error rather than picking one.
func statusAmbiguous(url string) Status {
	return Status{Severity: "ERROR", Code: "account:status:WorkspaceAmbiguous", Params: map[string]any{"workspace": url}}
}

// statusBadCredentials is the password-login refusal. The code is the platform
// status the SPA already translates ("Account not found or the provided
// credentials are incorrect") so the form renders a native message, and it
// deliberately does not distinguish no-such-account from wrong-password.
func statusBadCredentials() Status {
	return Status{Severity: "ERROR", Code: "platform:status:AccountNotFound", Params: map[string]any{}}
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
	// Every entry federates through the SAME IAM authorize hop: google/github
	// carry a provider_hint so hanzo.id lands STRAIGHT in the provider's OAuth
	// flow (the console-proven pattern, id >= 0.2.6); openid is the plain Hanzo
	// SSO page.
	return c.JSON(http.StatusOK, []ProviderInfo{
		{Name: "google", DisplayName: "Google"},
		{Name: "github", DisplayName: "GitHub"},
		{Name: g.cfg.provider, DisplayName: "Hanzo"},
	})
}

// ── REST: IAM OAuth bridge (external hop, net/http) ────────────────────────────

// authStart redirects the browser into IAM's authorize endpoint. team is a
// confidential client (client_secret), so no PKCE — the code is exchanged
// server-side in authCallback. state is a RANDOM nonce bound to a short-lived
// cookie (never the bare navigateUrl): the callback only proceeds when the two
// match, so a cross-site-initiated or replayed callback is refused.
func (g *api) authStart(c *zip.Ctx) error {
	// The redirect_uri is ALWAYS the canonical openid callback (the one IAM has
	// registered) — a /auth/google or /auth/github start differs only in the
	// provider_hint it carries into the authorize URL.
	origin := g.callbackOrigin(c)
	redirect := origin + "/v1/team/account/auth/" + g.cfg.provider + "/callback"
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
	if hint := providerHint(providerParam(c), c.Query("provider_hint")); hint != "" {
		q.Set("provider_hint", hint)
	}
	return c.Fiber().Redirect().Status(http.StatusFound).To(oauthBase(g.cfg.iamEndpoint) + "/oauth/authorize?" + q.Encode())
}

// providerHint is the ONE mapping from the SPA's provider path segment to the
// IAM provider_hint that makes hanzo.id auto-federate straight into the social
// provider (hint values are the IAM provider record names, e.g.
// "provider-github"). An explicit provider_hint query passes through verbatim;
// "openid" — the plain Hanzo SSO — carries none.
func providerHint(provider, explicit string) string {
	if explicit = strings.TrimSpace(explicit); explicit != "" {
		return explicit
	}
	switch provider {
	case "google", "github":
		return "provider-" + provider
	}
	return ""
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
	_, tok, failCode, err := g.establishSession(c.Context(), access)
	if err != nil {
		g.log.Error("account: establish session", "err", err)
		return g.bounce(c, "", navigate, failCode)
	}
	// Retain the IAM access_token (RS256) in an HttpOnly cookie so the same-origin
	// agents proxy can forward it to the cloud gateway. Page JS never reads it.
	g.setIAMTokenCookie(c, access)
	return g.bounce(c, tok, navigate, "")
}

// establishSession is the ONE post-authentication path: it turns a fresh IAM
// access token into a team session, identically for the OAuth callback and the
// password login. userinfo → canonical account id; tenant = the IAM org — the
// access token's `owner` claim, accepted ONLY off a VERIFIED token (RS256
// against the IAM JWKS, same trust anchor as the identity boundary). It scopes
// every workspace + data file — full multitenancy — so a verification failure
// fails CLOSED: no fallback org, no login. failCode is the OAuth bounce error
// code for the failing step.
func (g *api) establishSession(ctx context.Context, access string) (account, tok, failCode string, err error) {
	sub, email, name, err := g.userinfo(access)
	if err != nil {
		return "", "", "userinfo_failed", err
	}
	// AccountUuid = the IAM sub (derived to a stable UUID when the sub is not one).
	account = accountID(sub)
	id, err := g.verify(access)
	if err != nil {
		return "", "", "org_failed", err
	}
	if id.Owner == "" {
		return "", "", "org_failed", fmt.Errorf("verified token has empty owner")
	}
	org := id.Owner
	displayName := firstNonEmpty(name, localPart(email))
	// The VERIFIED membership set (home ∪ every org the token proves) is the ONE
	// source that drives BOTH the workspace union (getUserWorkspaces) AND the seat
	// projection (Seats). Ensuring a workspace — hence a counted member row — in
	// EVERY org the user belongs to, not just the home org, is what makes a
	// non-home org's wallet report the caller as a seat instead of "0 members".
	// A legacy token (iam < 1.31.34, empty claim) folds to the single home org.
	orgs := orgsClaim(id.Orgs, org)
	for _, o := range orgs {
		oorg, _ := o["org"].(string)
		if oorg == "" {
			continue
		}
		if _, err := g.accounts.EnsureWorkspace(ctx, oorg, account, displayName); err != nil {
			g.log.Error("account: ensure workspace", "org", oorg, "err", err)
		}
		// A new org gets its default office AND its default crew together: the
		// built-in @dev/@des/@vi personas, seeded once into the ONE agents registry
		// (idempotent, no-op without a model). Best-effort — a seed hiccup NEVER
		// blocks login; the crew simply appears on the next touch. They project into
		// the workspace roster as bot members (bots.go) and answer @-mentions through
		// the Chunter responder (chat.go), same as any org agent.
		if n, err := agents.SeedPersonalities(ctx, oorg); err != nil {
			g.log.Warn("account: seed personalities", "org", oorg, "err", err)
		} else if n > 0 {
			g.log.Info("account: seeded default crew", "org", oorg, "created", n)
		}
	}
	// Fill the human display name so the roster reconcile renders a name, not the
	// account uuid. Idempotent (only fills empty).
	_ = g.accounts.EnsureMemberName(ctx, account, displayName)

	// Carry the FULL membership set into the session token so getUserWorkspaces
	// can union a user's workspaces across every org they belong to (the Slack
	// model) with no IAM round-trip per poll — the SAME set just ensured above, so
	// the token, the workspace union, and the seat count never disagree. `org`
	// (home) is retained as the primary tenant every existing account-store
	// surface (files/collab/billing) already scopes to.
	extra := map[string]any{"org": org, "orgs": orgs}
	// extra.user is the IAM `<owner>/<name>` id — the key get-memberships takes for a
	// mid-session membership refresh. Present only when IAM gave a username.
	if id.Username != "" {
		extra["user"] = org + "/" + id.Username
	}
	tok, err = token.Generate(account, "", extra, expUnix(sessionTokenTTL), g.cfg.serverSecret)
	if err != nil {
		return "", "", "token_failed", err
	}
	return account, tok, "", nil
}

// orgsClaim builds the session token's extra.orgs value from the VERIFIED IAM
// `orgs` claim. It fails CLOSED to the single home org (sole admin) for a legacy
// token whose claim is empty, so the session ALWAYS carries at least the home
// tenant — never an empty set that would strand a user with zero workspaces. Each
// entry is a plain map so token.Generate's JSON marshal is stable and the decode
// side (orgsFromExtra) reads it back with no SDK dependency in the token layer.
func orgsClaim(orgs []iam.OrgRef, home string) []map[string]any {
	out := make([]map[string]any, 0, len(orgs)+1)
	seen := map[string]bool{}
	for _, o := range orgs {
		if o.Org == "" || seen[o.Org] {
			continue
		}
		seen[o.Org] = true
		out = append(out, map[string]any{"org": o.Org, "role": o.Role})
	}
	if len(out) == 0 && home != "" {
		out = append(out, map[string]any{"org": home, "role": "admin"})
	}
	return out
}

// orgsFromExtra reads the session token's extra.orgs back into the membership set.
// A legacy token (no orgs key) falls back to the single extra.org home tenant, so
// every authenticated path still resolves at least the home org. Deduped, home-safe.
func orgsFromExtra(extra map[string]any) []iam.OrgRef {
	out := make([]iam.OrgRef, 0, 4)
	seen := map[string]bool{}
	if raw, ok := extra["orgs"].([]any); ok {
		for _, e := range raw {
			m, ok := e.(map[string]any)
			if !ok {
				continue
			}
			org, _ := m["org"].(string)
			if org == "" || seen[org] {
				continue
			}
			seen[org] = true
			role, _ := m["role"].(string)
			out = append(out, iam.OrgRef{Org: org, Role: role})
		}
	}
	if len(out) == 0 {
		if org, _ := extra["org"].(string); org != "" {
			out = append(out, iam.OrgRef{Org: org, Role: "admin"})
		}
	}
	return out
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
	case "login":
		return g.passwordLogin(c, req.Params)
	case "getLoginInfoByToken", "getLoginWithWorkspaceInfo":
		return g.getLoginInfoByToken(c)
	case "getUserWorkspaces":
		return g.getUserWorkspaces(c)
	case "selectWorkspace":
		return g.selectWorkspace(c, req.Params)
	case "sendInvite":
		return g.sendInvite(c, req.Params)
	case "getMemberships":
		return g.getMemberships(c)
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
		return g.fail(c, Status{Severity: "ERROR", Code: "account:status:UnknownMethod", Params: map[string]any{"method": req.Method}})
	}
}

// passwordLogin is the account RPC "login" — the SPA's native email+password
// form. It authenticates against IAM ONLY (there are no local accounts): the
// SAME two-step the platform e2e auth helper documents — POST /v1/iam/login
// (responseType=code) with the credentials, then the standard confidential code
// exchange — then the EXACT session establishment the OAuth callback runs. The
// password lives in one local string, rides only in the body of the ONE IAM
// login call, and is never logged or persisted; bad credentials answer a clean
// 401 with the platform status the form already translates.
func (g *api) passwordLogin(c *zip.Ctx, params map[string]any) error {
	email, _ := params["email"].(string)
	password, _ := params["password"].(string)
	email = strings.TrimSpace(email)
	if email == "" || password == "" {
		return c.JSON(http.StatusUnauthorized, map[string]any{"error": statusBadCredentials()})
	}
	redirect := g.callbackOrigin(c) + "/v1/team/account/auth/" + g.cfg.provider + "/callback"
	code, err := g.passwordCode(email, password, redirect)
	if err != nil {
		// err carries IAM's status message only — never the submitted secret.
		g.log.Warn("account: password login refused", "err", err)
		return c.JSON(http.StatusUnauthorized, map[string]any{"error": statusBadCredentials()})
	}
	access, err := g.exchangeCode(code, redirect)
	if err != nil {
		g.log.Error("account: password login code exchange", "err", err)
		return c.JSON(http.StatusUnauthorized, map[string]any{"error": statusBadCredentials()})
	}
	account, tok, _, err := g.establishSession(c.Context(), access)
	if err != nil {
		g.log.Error("account: password login session", "err", err)
		return c.JSON(http.StatusUnauthorized, map[string]any{"error": statusBadCredentials()})
	}
	// Same cookie posture as the OAuth callback: the IAM access_token rides an
	// HttpOnly cookie for the same-origin agents proxy; the SPA then PUTs the
	// session cookie through the SAME /cookie route every login path uses.
	g.setIAMTokenCookie(c, access)
	return g.ok(c, LoginInfo{Account: account, Token: tok})
}

// passwordCode performs the IAM password login and returns the authorization
// code (the same two-step contract the platform e2e auth helper locks: the
// OAuth params ride the query string, the credentials ride the JSON body — the
// ONE place the password touches the wire). Error paths surface only IAM's
// status message.
func (g *api) passwordCode(username, password, redirectURI string) (string, error) {
	nonce, err := randState()
	if err != nil {
		return "", err
	}
	q := url.Values{
		"clientId":     {g.cfg.iamClientID},
		"responseType": {"code"},
		"redirectUri":  {redirectURI},
		"scope":        {"openid profile email"},
		"state":        {nonce},
		"type":         {"code"},
	}
	body, err := json.Marshal(map[string]any{
		"type":         "code",
		"application":  g.cfg.iamClientID,
		"username":     username,
		"password":     password,
		"signinMethod": "Password",
		"autoSignin":   true,
	})
	if err != nil {
		return "", err
	}
	resp, err := http.Post(oauthBase(g.cfg.iamEndpoint)+"/login?"+q.Encode(), "application/json", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("iam login request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("iam login status %d", resp.StatusCode)
	}
	var out struct {
		Status string          `json:"status"`
		Msg    string          `json:"msg"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("iam login decode: %w", err)
	}
	if out.Status != "ok" {
		return "", fmt.Errorf("iam login: %s", out.Msg)
	}
	var code string
	if err := json.Unmarshal(out.Data, &code); err != nil || code == "" {
		return "", fmt.Errorf("iam login: no authorization code in response")
	}
	return code, nil
}

func (g *api) getLoginInfoByToken(c *zip.Ctx) error {
	account, _, tok, err := g.account(c)
	if err != nil {
		return g.fail(c, statusUnauthorized(err.Error()))
	}
	return g.ok(c, LoginInfo{Account: account, Token: tok})
}

// getUserWorkspaces returns the UNION of the caller's workspaces across EVERY org
// in the session's membership set — the Slack model: a user who belongs to their
// home org plus one or more team orgs sees all of their workspaces in one list,
// each tagged with its owning org so the client switcher groups by org. Each
// WorkspacesOf join is still owner_org-scoped, so a user only ever sees workspaces
// they are a member of, and never a foreign tenant's.
func (g *api) getUserWorkspaces(c *zip.Ctx) error {
	account, orgs, _, err := g.accountOrgs(c)
	if err != nil {
		return g.fail(c, statusUnauthorized(err.Error()))
	}
	out := []WorkspaceInfo{}
	seen := map[string]bool{} // dedupe by workspace uuid (a ws belongs to one org)
	for _, o := range orgs {
		wss, err := g.accounts.WorkspacesOf(c.Context(), o.Org, account)
		if err != nil {
			return g.fail(c, statusError(err.Error()))
		}
		for _, ws := range wss {
			if seen[ws.UUID] {
				continue
			}
			seen[ws.UUID] = true
			out = append(out, toWorkspaceInfo(ws))
		}
	}
	return g.ok(c, out)
}

// selectWorkspace resolves the workspace the caller EXPLICITLY named (workspaceUrl)
// among EVERY org in the session's membership set, checks membership, then mints
// the workspace token carrying extra.org and returns the transactor wss endpoint.
//
// It NEVER defaults to a "first" workspace: an absent workspaceUrl is a clean
// BadRequest and a slug that resolves in two of the caller's orgs is a clean
// Ambiguous — the client must pass the explicit choice the /login/selectWorkspace
// selector already collects. The single-workspace fast path is the degenerate
// explicit case: when getUserWorkspaces returns exactly one workspace the front
// auto-selects it BY URL, so this path still receives an explicit workspaceUrl and
// the UX is unchanged. Cross-tenant isolation is preserved because each candidate
// lookup is owner_org-scoped to an org the session already proves membership in.
func (g *api) selectWorkspace(c *zip.Ctx, params map[string]any) error {
	account, orgs, _, err := g.accountOrgs(c)
	if err != nil {
		return g.fail(c, statusUnauthorized(err.Error()))
	}
	wsURL, _ := params["workspaceUrl"].(string)
	wsURL = strings.TrimSpace(wsURL)
	if wsURL == "" {
		return g.fail(c, statusBadRequest("workspaceUrl is required"))
	}
	ws, role, err := g.resolveWorkspace(c.Context(), orgs, account, wsURL)
	if err != nil {
		if err == errAmbiguousWorkspace {
			return g.fail(c, statusAmbiguous(wsURL))
		}
		return g.fail(c, statusWorkspaceNotFound(wsURL))
	}
	org := ws.OwnerOrg
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

// errAmbiguousWorkspace is returned by resolveWorkspace when a slug the caller is
// a member of exists in MORE THAN ONE of the session orgs — the caller must
// disambiguate rather than have the server silently pick one.
var errAmbiguousWorkspace = fmt.Errorf("team: workspace slug ambiguous across orgs")

// resolveWorkspace maps an EXPLICIT (slug) to the single workspace the caller is a
// member of across the session's org set. It is the one place the cross-org lookup
// lives: iterate the caller's orgs, resolve the slug owner_org-scoped in each, keep
// only those the caller is a member of, and require EXACTLY one — 0 ⇒ not found,
// >1 ⇒ ambiguous. Never a silent default. Returns the workspace and the caller's
// role in it.
func (g *api) resolveWorkspace(ctx context.Context, orgs []iam.OrgRef, account, slug string) (workspace, Role, error) {
	var found workspace
	var role Role
	n := 0
	seen := map[string]bool{}
	for _, o := range orgs {
		if o.Org == "" || seen[o.Org] {
			continue
		}
		seen[o.Org] = true
		ws, err := g.accounts.WorkspaceBySlug(ctx, o.Org, slug)
		if err != nil {
			continue // absent in this org (or a real store error) — not a candidate
		}
		r, ok := g.accounts.Membership(ctx, ws.ID, account)
		if !ok {
			continue // resolvable but the caller is not a member — not a candidate
		}
		found, role = ws, r
		n++
	}
	switch {
	case n == 0:
		return workspace{}, "", errNoWorkspace
	case n > 1:
		return workspace{}, "", errAmbiguousWorkspace
	default:
		return found, role, nil
	}
}

// getWorkspaceInfo returns info for THE workspace the caller is scoped to — the
// one selectWorkspace already minted into the session token's `workspace` claim,
// resolved owner_org-scoped by (org, uuid). It NEVER falls back to the caller's
// first workspace: a token with no workspace claim (an account/login token that
// has not selected a workspace yet) is a clean WorkspaceNotFound, so the client is
// forced through the explicit selectWorkspace step rather than being silently
// handed an arbitrary one.
func (g *api) getWorkspaceInfo(c *zip.Ctx) error {
	t, _, err := sessionToken(c, g.cfg.serverSecret)
	if err != nil {
		return g.fail(c, statusUnauthorized(err.Error()))
	}
	if t.Workspace == "" {
		return g.fail(c, statusWorkspaceNotFound(""))
	}
	org, _ := t.Extra["org"].(string)
	ws, err := g.accounts.WorkspaceByUUID(c.Context(), org, t.Workspace)
	if err != nil {
		return g.fail(c, statusWorkspaceNotFound(t.Workspace))
	}
	return g.ok(c, toWorkspaceInfo(ws))
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

// accountOrgs resolves (AccountUuid, membership set, token) from the verified
// session token. The set is the SIGNED extra.orgs claim (home + every team org),
// read back home-safe by orgsFromExtra — the tenant SET the cross-org surfaces
// (getUserWorkspaces union, selectWorkspace resolution) enumerate. Never a client
// header. Empty account fails closed, exactly like account().
func (g *api) accountOrgs(c *zip.Ctx) (account string, orgs []iam.OrgRef, tok string, err error) {
	t, raw, err := sessionToken(c, g.cfg.serverSecret)
	if err != nil {
		return "", nil, "", err
	}
	return t.Account, orgsFromExtra(t.Extra), raw, nil
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
		UUID: ws.UUID, Name: ws.Name, URL: ws.Slug, DataID: ws.DataID, Org: ws.OwnerOrg, Region: ws.Region,
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
