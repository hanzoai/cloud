package team

// This file is the account API the team SPA speaks to at
// /v1/team/account — JSON-RPC over a single POST, plus the /providers,
// /auth/{provider} and /cookie REST siblings. It is the FULL REWRITE of
// github.com/hanzoai/team/pkg/account (account.go + types.go): the Base-DAO
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
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/agents"
	"github.com/hanzoai/cloud/apps/team/token"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/types"
	model "github.com/hanzoai/iam/pkg/model"
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
	// ident is the identity seam every team surface resolves its caller through:
	// cloud's RS256/JWKS IAM validator (the SAME trust anchor as the identity
	// boundary), the HS256 secret, and the membership rows. The OAuth callback
	// derives its tenant from that validator's verdict, never from unverified
	// claims — one validator, not a second copy beside the seam holding it.
	ident *identity
	// commerce answers CheckEntitlement(org, "team") at workspace select — nil
	// (not co-resident) is an infra absence and never blocks login.
	commerce types.CommerceClient
	// planEnt resolves a plan id to its entitlement block (plan.Entitlements) —
	// the source of the team.guests cap.
	planEnt func(context.Context, string) (map[string]any, error)
	// degraded is the fail-closed posture Mount resolved (no HS256 secret). The
	// untyped routes get it through Mount's guard wrapper; a typed op is not a
	// zip.Handler and cannot be wrapped, so it reads this instead (typed.go).
	degraded bool
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
	// Name is the provider id, and it is the value that goes back in the URL to
	// start a login: GET /v1/team/account/auth/{provider}. This deployment
	// surfaces exactly one, "openid" — the hanzo.id door.
	Name string `json:"name"`
	// DisplayName is the human label for the sign-in button; this deployment
	// sends "Hanzo". Omitted from the body when empty.
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

// signInAtIssuer is the refusal every credential verb answers with. One string,
// so the door's answer is a single fact a test can pin.
const signInAtIssuer = "sign in at hanzo.id"

// trunc bounds a caller-supplied string before it rides back in an error. The RPC
// body is only capped by GATEWAY_BODY_LIMIT (16MB), so echoing a method name
// verbatim lets a caller choose the size of our response.
func trunc(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
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

// ── route registration ────────────────────────────────────────────────────────

// The prose for this file's four UNTYPED routes — the RPC envelope and the
// three browser-shaped ones (two redirects and a Set-Cookie). None of them is a
// value a typed In/Out can carry, so zipdoc has nothing to lift and without
// this they publish an operationId and nothing else. The typed siblings
// (/account/providers, DELETE /account/cookie) document themselves from their
// own doc comments.
//
// The path keys are the FIBER patterns exactly as registered below.
func init() {
	openapi.Describe("/v1/team/account", http.MethodPost,
		"Read the caller's account and switch workspace",
		"The account control plane the Team client speaks: one POST carries a `method` verb "+
			"and its `params`, and answers {\"result\": …}. The verbs are the session's own reads "+
			"and the workspace switch — getLoginInfoByToken, getUserWorkspaces, selectWorkspace, "+
			"getWorkspaceInfo, getMemberships, getPerson, getSocialIds, getRegionInfo, "+
			"isReadOnlyGuest — plus sendInvite, which adds a member to a workspace and is "+
			"refused for a caller who is not its owner or admin.\n\n"+
			"A REFUSAL IS HTTP 200 carrying {\"error\": {severity, code, params}} — the platform "+
			"Status the client translates — not a 4xx. An unreadable body, an unauthorized "+
			"session and an unknown verb all arrive that way, so a caller that reads only the "+
			"status code reads every failure here as a success.\n\n"+
			"NO CREDENTIAL IS EVER HANDLED HERE. login, signUp, the OTP verbs, password change "+
			"and reset, join and the guest-token exchange each answer Unauthorized with \"sign "+
			"in at hanzo.id\" — a stated policy, not an unknown method, so the door being shut "+
			"is a fact a test can pin. Sessions come from the OAuth pair under /account/auth.\n\n"+
			"Auth is the team session token: Authorization: Bearer, else the HttpOnly "+
			"account-token cookie. The tenant is that token's SIGNED org claim, never a header, "+
			"and selectWorkspace resolves only among the orgs the token proves membership of. "+
			"It also demands an explicit workspaceUrl — it never falls back to a first "+
			"workspace, and a slug that resolves in two of the caller's orgs answers Ambiguous "+
			"rather than picking one.")
	openapi.Describe("/v1/team/account/auth/:provider", http.MethodGet,
		"Start a sign-in at hanzo.id",
		"STARTS the OAuth hop: answers 302 to hanzo.id's authorize endpoint and sets the "+
			"short-lived HttpOnly state cookie that binds the flow to this browser. NO TOKEN "+
			"COMES BACK FROM THIS CALL — the session is minted by the callback below, and a "+
			"client that expects JSON here gets a redirect with no body.\n\n"+
			"A browser is the intended caller. Anything else must follow the Location AND keep "+
			"the Set-Cookie, because the callback refuses a flow whose state it cannot match. "+
			"That cookie carries the random nonce plus the client's navigateUrl, so the round "+
			"trip needs no second channel, and it lives ten minutes — the whole budget for the "+
			"hop.\n\n"+
			"The provider segment only picks a hint: the redirect_uri is ALWAYS the canonical "+
			"openid callback, the one IAM has registered. Measured end to end, hanzo.id strips "+
			"that hint today, so /auth/google and /auth/openid land on the same Hanzo sign-in "+
			"page — the federation shortcut is an upstream fix, not a second door here.")
	openapi.Describe("/v1/team/account/auth/:provider/callback", http.MethodGet,
		"Complete a sign-in and hand the browser its session",
		"COMPLETES the OAuth hop: hanzo.id redirects the browser here with ?code and ?state, "+
			"and the answer is another 302 — back to the client's login route carrying the "+
			"freshly minted team session token in the query. Never JSON, and never a token in "+
			"this response's own body.\n\n"+
			"THE STATE IS CHECKED FIRST, before the code is even looked at: the flow cookie is "+
			"read and cleared one-shot, and a callback whose ?state does not equal the nonce it "+
			"held is bounced with error=state_mismatch and NEVER exchanged. That is what makes "+
			"a forged or replayed callback inert. Only then is the code exchanged server-side — "+
			"team is a confidential client with a client_secret, so there is no PKCE and the "+
			"code never passes through the browser's JS.\n\n"+
			"The tenant is derived from the IAM access token VERIFIED RS256 against the JWKS, "+
			"the same trust anchor the identity boundary uses; a token whose owner claim is "+
			"empty fails closed with no login at all. Every org that token proves gets a "+
			"workspace ensured, so a member of two orgs is a counted seat in both. The IAM "+
			"access token is also parked in an HttpOnly cookie for the same-origin agents "+
			"proxy — page JS never reads it.\n\n"+
			"EVERY failure is a redirect, not a status: a denied consent, a missing code, a "+
			"failed exchange, an unreadable userinfo, an unverifiable org and a token-mint "+
			"failure each bounce to the login page with an ?error code naming the step.")
	openapi.Describe("/v1/team/account/cookie", http.MethodPut,
		"Store the session token as this browser's cookie",
		"Writes the team session token into the HttpOnly `account-token` cookie — Secure, "+
			"SameSite=Lax, whole-origin scope, thirty days — and answers {\"result\": true}. This "+
			"is how the client turns the token it caught off the OAuth bounce into a credential "+
			"page JS can no longer read, which IS the security property: script that cannot see "+
			"the cookie cannot exfiltrate it, and every later call on the files, billing and "+
			"collaborator planes authenticates from it when no bearer is sent.\n\n"+
			"The token is VERIFIED — signature and expiry, against this service's own signing "+
			"secret — BEFORE it is stored. Anything this service did not sign is 401 and "+
			"nothing is written; persisting a caller-supplied value unchecked would be a "+
			"session-fixation door, where an attacker pins a cookie the victim's browser then "+
			"presents as its own.\n\n"+
			"The token may arrive as `token` in the JSON body or, when the body is absent or "+
			"unparseable, from the Authorization bearer — an unreadable body is NOT an error "+
			"here. The sibling DELETE clears this same cookie and signs the browser out of team "+
			"only: the IAM cookie set alongside it is a different credential with its own "+
			"lifetime and is left alone.")
}

func (g *api) register(app cloud.Router, guard guardFn) {
	// The group is built HERE so cmd/zipdoc can resolve the typed op's prefix from
	// this file — see bots.go for why.
	r := app.Group(teamPrefix)
	// UNTYPED, and it cannot be otherwise: this is a JSON-RPC envelope. The verb
	// is a body field, the `result` is a different shape per verb, a refusal is
	// HTTP 200 carrying {error: Status} — including for an unparseable body — and
	// the entitlement arm answers 402 with a second key. A typed In turns that 200
	// into a 400 and a typed Out can only say `any`, so typing it would move the
	// wire and describe nothing.
	r.Post("/account", guard(g.rpc))
	// TYPED: /providers takes nothing and answers a fixed list, so it is the one
	// account route that is a whole op rather than one verb of the RPC or a
	// browser redirect. A typed op is not a zip.Handler and cannot be wrapped by
	// guard, so it carries the degraded refusal itself (g.degraded, typed.go).
	zip.Get(r, "/account/providers", g.listProviders)
	// UNTYPED: both are browser REDIRECTS — 302 + Location + Set-Cookie, no body
	// at all. A typed op answers a JSON value under a 2xx, which is a different
	// response.
	r.Get("/account/auth/:provider", guard(g.authStart))
	r.Get("/account/auth/:provider/callback", guard(g.authCallback))
	// UNTYPED: an unparseable body is IGNORED here — the token falls back to the
	// Authorization bearer, and the request succeeds. zip decodes a typed In
	// before the handler runs and answers 400, so typing this one would refuse a
	// request it has always served.
	r.Put("/account/cookie", guard(g.setCookie))
	// TYPED: a DELETE addresses what it deletes with its URL and reads no body,
	// which is exactly what this one already did.
	zip.Delete(r, "/account/cookie", g.clearCookie)
}

// ── REST: providers ───────────────────────────────────────────────────────────

// providerList is the GET /providers body: a bare JSON array of the identity
// providers the SPA may start a login with. Named (not a plain []ProviderInfo)
// so the document can describe the response at all — zip declares a response
// schema only for an Out type that HAS a name.
type providerList []ProviderInfo

// ListProviders returns the identity providers this deployment starts a login
// with. It is always exactly one — hanzo.id. Which identities that door accepts
// (Google, GitHub, passkey, password) is IAM's question, answered on IAM's own
// page next to the identity check and the training-data consent that must
// precede a first session; listing them here would be a second place holding
// that answer, and the two drift the moment IAM gains or drops one.
//
// Response: [{"name": "openid", "displayName": "Hanzo"}]
func (g *api) listProviders(ctx context.Context, _ *none) (*providerList, error) {
	if g.degraded {
		return nil, unavailable()
	}
	return &providerList{{Name: g.cfg.provider, DisplayName: "Hanzo"}}, nil
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
// IAM provider_hint param (hint values are the IAM provider record names, e.g.
// "provider-github"). An explicit provider_hint query passes through verbatim;
// "openid" — the plain Hanzo SSO — carries none.
//
// MEASURED: the hint is currently a NO-OP end to end, and the gap is upstream.
// hanzo.id's login app does honour it, but the endpoint authStart calls strips
// it first:
//
//	GET /v1/iam/oauth/authorize?...&provider_hint=provider-google
//	  -> 302 /login/oauth/authorize?client_id&redirect_uri&response_type&scope&state
//	                                                        (no provider_hint)
//
// The Location is byte-identical with and without the param. So /auth/google
// lands on the same Hanzo SSO page as /auth/openid. That is not a hole — every
// path still runs IAM's authorize flow — it just buys nothing today. If the
// federation shortcut is wanted, the fix is param passthrough in IAM's
// /v1/iam/oauth/authorize, NOT more code here.
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
	id, err := g.ident.verify(access)
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
// `orgs` claim. The home tenant is ALWAYS present: it is extra.org — the org the
// wallet, Seats, and every account-store surface scope to — so its workspace has
// to be ensured (and count a seat) at login. The IAM orgs claim does not reliably
// list a user's OWN home org (it may carry only explicit team memberships), so
// home is appended when absent rather than only when the claim is empty; without
// this a user whose claim names other orgs but not home lands on a home org with
// no ensured workspace and a wallet that reports 0 seats. Each entry is a plain
// map so token.Generate's JSON marshal is stable and the decode side
// (orgsFromExtra) reads it back with no SDK dependency in the token layer.
func orgsClaim(orgs []model.OrgRef, home string) []map[string]any {
	refs := homeOrgs(orgs, home)
	out := make([]map[string]any, 0, len(refs))
	for _, o := range refs {
		out = append(out, map[string]any{"org": o.Org, "role": o.Role})
	}
	return out
}

// homeOrgs is that rule itself: the verified membership set, deduped, with the home
// tenant guaranteed present. It is shared by the login mint above and by the IAM
// lane's caller (identity.iam), so a person enumerates the SAME orgs whichever
// credential they arrive with.
func homeOrgs(orgs []model.OrgRef, home string) []model.OrgRef {
	out := make([]model.OrgRef, 0, len(orgs)+1)
	seen := map[string]bool{}
	for _, o := range orgs {
		if o.Org == "" || seen[o.Org] {
			continue
		}
		seen[o.Org] = true
		out = append(out, o)
	}
	if home != "" && !seen[home] {
		out = append(out, model.OrgRef{Org: home, Role: "admin"})
	}
	return out
}

// orgsFromExtra reads the session token's extra.orgs back into the membership set.
// A legacy token (no orgs key) falls back to the single extra.org home tenant, so
// every authenticated path still resolves at least the home org. Deduped, home-safe.
func orgsFromExtra(extra map[string]any) []model.OrgRef {
	out := make([]model.OrgRef, 0, 4)
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
			out = append(out, model.OrgRef{Org: org, Role: role})
		}
	}
	if len(out) == 0 {
		if org, _ := extra["org"].(string); org != "" {
			out = append(out, model.OrgRef{Org: org, Role: "admin"})
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
	//
	// The HS256 arm ONLY, deliberately: this writes account-token, and the IAM
	// cookie beside it is minted by the OAuth callback out of a code exchange the
	// browser itself started. Accepting a caller-supplied IAM token here would add a
	// second, caller-driven writer for that cookie — a session-fixation surface the
	// callback does not have.
	if _, err := g.ident.hs256(body.Token); err != nil {
		return zip.ErrUnauthorized("invalid session token")
	}
	g.setSessionCookie(c, authCookie, body.Token, int(sessionTokenTTL.Seconds()))
	return c.JSON(http.StatusOK, map[string]any{"result": true})
}

// cookieAck is the account-cookie plane's acknowledgement — the {"result": true}
// the SPA's Auth reads back from a cookie write or clear.
type cookieAck struct {
	// Result is true when the cookie was written or cleared.
	Result bool `json:"result"`
}

// ClearCookie signs this browser out of team by expiring the HttpOnly
// account-token cookie the OAuth callback set. It is the counterpart of the
// cookie PUT, it takes nothing — the cookie it clears is named by this service,
// never by the caller — and it is unconditional: a caller with no cookie, an
// expired one or a forged one all get the same acknowledgement, because clearing
// something that is not there is the same outcome as clearing something that is.
//
// It clears ONLY the team session cookie. The IAM access-token cookie the same
// callback set is a different credential with a different lifetime and is left
// alone, so this is a team sign-out, not a platform one.
func (g *api) clearCookie(ctx context.Context, _ *none) (*cookieAck, error) {
	if g.degraded {
		return nil, unavailable()
	}
	if !g.cookie(ctx, authCookie, "", -1) {
		// No request means no response to clear a cookie on, and no browser that
		// could have been signed in — so there is nothing to honestly acknowledge.
		return nil, zip.ErrBadRequest("no HTTP response to clear the cookie on")
	}
	return &cookieAck{Result: true}, nil
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

	// Every verb that would establish a session from a credential this service
	// handled itself. Stating the policy beats falling through to UnknownMethod:
	// that answer renders "Unknown method: login" to the user, tells a caller the
	// parser did not recognise the verb rather than that the door is shut, and —
	// because "no handler" and "handler refused" then produce the same envelope —
	// makes a resurrected handler indistinguishable from a deleted one. A test can
	// pin THIS answer; it cannot pin an absence.
	case "login", "loginAsGuest", "loginOtp", "signUp", "signUpOtp", "signUpJoin",
		"validateOtp", "join", "joinByToken", "exchangeGuestToken",
		"changePassword", "restorePassword", "requestPasswordReset",
		"confirm", "createAccessLink", "checkJoin", "checkAutoJoin",
		"refreshHanzoAssistantToken":
		return g.fail(c, statusUnauthorized(signInAtIssuer))

	default:
		return g.fail(c, Status{Severity: "ERROR", Code: "account:status:UnknownMethod", Params: map[string]any{"method": trunc(req.Method, 64)}})
	}
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
	// Carry the tenant AND the caller's role into the workspace token so the
	// transactor routes to orgs/<org>/ws/<workspace>.db and every downstream holder
	// can tell a member from a guest. Short-lived (workspaceTokenTTL) — it rides in
	// the transactor URL path, so a bounded lifetime caps replay on capture.
	//
	// extra.role is the ONLY place a reduced principal is expressible on the wire.
	// resolveWorkspace already returned it and this mint used to DROP it, so every
	// consumer of a workspace token saw an owner and a guest as identical — and
	// entitle() cannot help, being a billing gate that returns nil on every branch by
	// design (observe mode). Signing it means clients/analytics and clients/meet
	// decide capability from a verified claim, with no DB hop and no reach into this
	// package's store. token.Privileged() is the one predicate that reads it.
	wsTok, err := token.Generate(account, ws.UUID, map[string]any{"org": org, "role": role}, expUnix(workspaceTokenTTL), g.cfg.serverSecret)
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
func (g *api) resolveWorkspace(ctx context.Context, orgs []model.OrgRef, account, slug string) (workspace, Role, error) {
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

// getWorkspaceInfo returns info for THE workspace the caller's CREDENTIAL is
// scoped to — the one selectWorkspace already minted into the workspace token's
// `workspace` claim, resolved owner_org-scoped by (org, uuid). It NEVER falls back
// to the caller's first workspace: a credential that pins no workspace (an
// account/login token that has not selected one, and every IAM caller, which pins
// nothing by construction) is a clean WorkspaceNotFound, so the client is forced
// through the explicit selectWorkspace step rather than being silently handed an
// arbitrary one.
func (g *api) getWorkspaceInfo(c *zip.Ctx) error {
	cl, err := g.ident.who(c)
	if err != nil {
		return g.fail(c, statusUnauthorized(err.Error()))
	}
	if cl.workspace == "" {
		return g.fail(c, statusWorkspaceNotFound(""))
	}
	ws, err := g.accounts.WorkspaceByUUID(c.Context(), cl.org, cl.workspace)
	if err != nil {
		return g.fail(c, statusWorkspaceNotFound(cl.workspace))
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

// ── the identity seam ─────────────────────────────────────────────────────────

// identity is what every team surface turns a credential into a caller with, and
// the ONE place a credential's algorithm is routed on. It composes three answers
// and braids none of them: VERIFICATION (an IAM access token against the IAM JWKS,
// or the HS256 signature), ACCOUNT RESOLUTION (accountID over the IAM subject —
// the join establishSession stores the account's rows under), and WORKSPACE
// AUTHORIZATION (the membership rows, admit).
type identity struct {
	// verify is cloud's RS256/JWKS IAM validator (cloud.NewTokenValidator) — the
	// SAME trust anchor as the identity boundary and as the OAuth callback's tenant
	// derivation, so a token any one of them accepts is a token all three accept.
	verify func(string) (cloud.VerifiedIdentity, error)
	// secret is SERVER_SECRET, the key of the HS256 arm.
	secret string
	// accounts is the membership authority. On the IAM lane nothing about a
	// workspace is signed, so these rows ARE the authorization.
	accounts *accountStore
	// audience is the set of IAM apps whose access tokens this deployment accepts
	// as a TEAM SESSION. See identity.iam for why team gates on it when the
	// identity boundary deliberately does not.
	audience map[string]bool
}

// caller is who a team surface is talking to. It is the WHOLE answer: no surface
// reads a claim off a credential for itself, so no surface can disagree with this
// one about who is calling.
type caller struct {
	// account is the team AccountUuid.
	account string
	// org is the IAM tenant every account-store query is scoped to.
	org string
	// orgs is the home-safe membership set the cross-org surfaces enumerate.
	orgs []model.OrgRef
	// user is the IAM `<owner>/<name>` id, the key IAM's get-user takes for a
	// mid-session membership refresh. Empty when the credential names no username.
	user string
	// workspace is the workspace the CREDENTIAL pinned itself to. Empty on the IAM
	// lane, which pins nothing: what an IAM caller may touch is decided per request
	// by admit against the rows, never by a claim the caller carries.
	workspace string
	// raw is the HS256 credential exactly as presented, and it is EMPTY ON THE IAM
	// LANE — deliberately, structurally, and not as a rule each caller remembers.
	//
	// The account RPC echoes this back to the SPA as its session token, and the SPA
	// is page JS. An IAM access token is an estate-wide RS256 bearer that reaches
	// the gateway, KMS and every other service; the login flow puts it in an
	// HttpOnly cookie precisely so script can never read it. Echoing it here would
	// hand it straight back to the script the cookie flag exists to keep it from —
	// one unauthenticated-looking RPC, and the caller's whole platform credential is
	// in a variable. So the IAM lane carries no credential OUT of this file at all,
	// and a future echo site cannot reintroduce the leak by forgetting.
	raw string
	// iam reports which lane resolved this caller. It exists so a surface can grant
	// on rows instead of on a signed workspace claim, not so it can re-derive trust.
	iam bool
}

// who resolves the caller of a team surface, on either of two lanes.
//
// THE IAM LANE is an IAM access token — Authorization: Bearer, else the
// hanzo_iam_token cookie the login flow already set — verified against the IAM
// JWKS, narrowed to this deployment's own audience, and resolved to a team account
// through the store by its SUBJECT (identity.iam).
//
// THE HS256 ARM is the token this service minted, semantics unchanged: bearer
// first and the account-token cookie after, signature and exp/nbf enforced. It is
// deleted when login mints IAM-only and front/love/analytics-collector verify IAM.
//
// ONE SURFACE IS NOT DUAL-READ YET, and it blocks that deletion: getWorkspaceInfo
// answers for the workspace the CREDENTIAL pins, and the IAM lane pins none by
// construction — only selectWorkspace's HS256 mint does. So the workspace a client
// is "in" still has to travel as a claim. Deleting the arm means the front NAMING
// the workspace on that call (as it already does for selectWorkspace) and this
// authorizing it through admit, the same way the transactor and files planes
// already do. That is a client change, which is why it is a later phase and not
// this one.
//
// THE ORDER IS WHAT MAKES THIS PHASE INERT, and it is the existing credential
// first on BOTH carriers:
//
//   - Authorization is answered by the bearer alone. A signed-in browser carries an
//     IAM cookie beside its HS256 bearer, so consulting the cookie for a request
//     that already presented a bearer would move every current client onto the new
//     lane at once.
//   - with no bearer, account-token is read BEFORE hanzo_iam_token, and an
//     account-token that is PRESENT answers alone — a stale one is refused rather
//     than falling through. The two cookies coexist for the whole overlap and are
//     not interchangeable: the HS256 one can PIN A WORKSPACE and the IAM one
//     cannot, so preferring the IAM cookie silently widened the collaborator planes
//     from "the workspace this token names" to "any workspace you are a member of",
//     and made getWorkspaceInfo answer WorkspaceNotFound where the pin used to
//     answer. Falling through on expiry would be the same widening on a timer: a
//     session that used to end in a 401 would quietly continue with a different
//     reach.
//
// So the rule is one sentence for every carrier: THE FIRST CREDENTIAL THE REQUEST
// PRESENTS, IN CARRIER ORDER, IS THE ONE THAT ANSWERS. The IAM cookie is reached by
// a browser holding nothing else, which is exactly the post-cutover client and
// nobody today — which is what makes this phase inert. Within a carrier IAM wins: a
// header that verifies as IAM is never re-read as HS256.
func (id *identity) who(c *zip.Ctx) (caller, error) {
	if id == nil {
		return caller{}, fmt.Errorf("no identity seam")
	}
	ctx := c.Context()
	if raw := bearer(c); raw != "" {
		return id.verified(ctx, raw)
	}
	if raw := c.Fiber().Req().Cookies(authCookie); raw != "" {
		return id.hs256(raw)
	}
	return id.iam(ctx, c.Fiber().Req().Cookies(iamTokenCookie))
}

// verified is who() over ONE presented credential rather than over a request's
// carriers — the same two lanes in the same order, for the surfaces that carry the
// credential in a body or a path segment instead of a header. The HS256 error is
// the one reported: both arms fail closed, so the caller learns why the credential
// it actually holds was refused rather than why the other lane did not claim it.
func (id *identity) verified(ctx context.Context, raw string) (caller, error) {
	if cl, err := id.iam(ctx, raw); err == nil {
		return cl, nil
	}
	return id.hs256(raw)
}

// iam turns a VERIFIED IAM ACCESS token into a caller. Fails closed on every
// path: no validator, no store to resolve against, an unverifiable token, one that
// is not an access token, one whose owner claim is empty (there is no tenant to
// scope to), and one whose SUBJECT names no account in that tenant.
//
// THE SUBJECT, NEVER THE CANONICAL USER ID. VerifiedIdentity.User falls back sub →
// preferred_username → name, so a token carrying no sub presents its USERNAME
// there — and accountID returns a UUID-shaped input verbatim, so a username set to
// a colleague's account uuid resolved to the colleague, and admit() then granted
// every workspace the two share. Subject-only closes it; the account itself comes
// from the store (AccountForSubject), which confirms the row a login created
// rather than asserting an id no row has to match.
//
// TYPE, NOT JUST SIGNATURE. IAM's signer emits the same claim set into the access
// token and the id_token but for aud/tokenType/nonce (middleware_identity.go), so
// a valid signature from a trusted issuer does not say WHICH of them arrived — and
// the id_token is the one handed to a browser SPA to read. A session credential
// must be the access token, so the type is checked here.
//
// AUDIENCE IS CHECKED HERE, and it is checked here BECAUSE the identity boundary
// deliberately does not. That posture was decided for the boundary, whose job is
// "did IAM mint this for one of its own apps" — for an API call, aud only names
// which app, and cloud kept no mirror of IAM's registry because the mirror drifted
// and silently 401'd every new first-party app. A SESSION is a different question.
// This lane turns a bearer into a signed-in person on hanzo.team, and a token the
// user obtained for a DIFFERENT app — chat, the console, any OIDC client they ever
// clicked through — is not consent to that. Without the gate, one app's token is
// every app's session, which is the confused-deputy shape the estate closes
// elsewhere by narrowing at the resource server rather than at the door.
//
// The set is this deployment's OWN client id and nothing else by default, so it
// cannot drift into a registry mirror: it is one value team already has to know to
// run its OAuth flow, and the browser's hanzo_iam_token is the token that flow
// exchanged, so it carries exactly this audience. Additional first-party SPAs are
// named explicitly by an operator (TEAM_IAM_AUDIENCES) rather than admitted by a
// pattern — an audience allowlist that grows by rule is the mirror again.
//
// TENANT IS THE HOME ORG, NEVER `owner`. v.Owner carries the APPLICATION's org, so
// it is chosen by whichever app the caller authenticated through; a token with
// owner="lux" and a membership set naming hanzo would otherwise scope every team
// store query to "lux". The boundary refuses to derive a tenant from that claim
// (idClaims.homeOrg) and so does this. An empty home is a refusal, which also
// excludes every MACHINE credential — a client_credentials app or an API key is a
// member of nothing, and a team session is a person's.
func (id *identity) iam(ctx context.Context, raw string) (caller, error) {
	if id == nil || id.verify == nil {
		return caller{}, fmt.Errorf("no iam validator")
	}
	if id.accounts == nil {
		return caller{}, fmt.Errorf("no account store to resolve a subject against")
	}
	if raw == "" {
		return caller{}, fmt.Errorf("no token")
	}
	v, err := id.verify(raw)
	if err != nil {
		return caller{}, err
	}
	if !isAccessToken(v.TokenType) {
		return caller{}, fmt.Errorf("not an access token: tokenType %q", v.TokenType)
	}
	if !id.forThisDeployment(v.Audience) {
		return caller{}, fmt.Errorf("token audience %v is not a team session audience", v.Audience)
	}
	org := v.Home()
	if org == "" {
		return caller{}, fmt.Errorf("verified token names no home org")
	}
	if v.Subject == "" {
		return caller{}, fmt.Errorf("verified token carries no subject")
	}
	account, ok := id.accounts.AccountForSubject(ctx, org, v.Subject)
	if !ok {
		return caller{}, fmt.Errorf("verified subject holds no account in %q", org)
	}
	user := ""
	if v.Username != "" {
		user = org + "/" + v.Username
	}
	// NO raw: an IAM credential never leaves this function. See caller.raw.
	return caller{
		account: account,
		org:     org,
		orgs:    homeOrgs(v.Orgs, org),
		user:    user,
		iam:     true,
	}, nil
}

// forThisDeployment reports whether a token was minted for an app whose session
// this deployment is. Empty audience is REFUSED: a session credential that names
// no app is one nobody consented to hand here.
//
// THE INCOMING CLAIM IS MATCHED EXACTLY — no trim, no fold, no normalisation.
// Normalising it here would make the comparison non-injective: "hanzo-team " and
// "hanzo-team" are DISTINCT IAM applications (IAM refuses only an exact name
// collision, so the padded one is registrable), and trimming collapses them onto
// one key, handing every session of the real app to whoever registered the
// lookalike. This is the rule OrgHasUnsafeRune states for orgs — an injective
// boundary must never fold two distinct identifiers into one — applied to the
// identifier this door happens to compare.
//
// Whitespace is dealt with once, on the way IN, where the set is BUILT
// (sessionAudience): an operator's config entry is theirs to tidy, a signed claim
// is not ours to rewrite.
func (id *identity) forThisDeployment(aud []string) bool {
	for _, a := range aud {
		if id.audience[a] {
			return true
		}
	}
	return false
}

// sessionAudience is the set of IAM apps whose access tokens this deployment
// accepts as a team session: its OWN client id, plus any explicitly named by the
// operator in TEAM_IAM_AUDIENCES (comma-separated).
//
// The default is one value — the client id team already needs to run its OAuth
// flow, and therefore the audience of the very token that flow puts in the
// browser's cookie. Extra entries are NAMED, never matched by a pattern: an
// audience set that grows by rule is the IAM app-registry mirror the estate
// deleted, arriving one wildcard at a time.
//
// Trimming happens HERE and only here — an operator's config entry is theirs to
// tidy, while the signed claim this set is compared against is matched exactly
// (see identity.forThisDeployment for why folding it is a hole).
//
// Phase-2 precondition: if the hanzo-team IAM app is IsShared, seed
// clientID+"-org-"+<org> here — a shared app's access tokens carry the per-org
// audience form.
func sessionAudience(cfg config) map[string]bool {
	out := map[string]bool{}
	if id := strings.TrimSpace(cfg.iamClientID); id != "" {
		out[id] = true
	}
	for a := range strings.SplitSeq(os.Getenv("TEAM_IAM_AUDIENCES"), ",") {
		if a = strings.TrimSpace(a); a != "" {
			out[a] = true
		}
	}
	return out
}

// isAccessToken reports whether IAM's `tokenType` names the token a bearer session
// may be built on.
//
// The comparison is case-insensitive and treats an ABSENT type as an access token,
// which is the one permissive branch here and is deliberate: IAM has minted tokens
// without the claim, and refusing those would sign every one of those users out at
// deploy rather than at expiry. It is safe in the direction that matters — the
// id_token this exists to exclude is exactly the one that DOES carry a type, so an
// omitted claim is never an id_token being waved through. It stops being reached as
// tokens roll over, rather than needing a flag day.
func isAccessToken(t string) bool {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case "", "access-token", "access_token", "bearer":
		return true
	default:
		return false
	}
}

// hs256 decodes AND verifies (signature + expiry) the HS256 session or workspace
// token this service minted. The tenant, the membership set and the workspace all
// come from its SIGNED claims.
func (id *identity) hs256(raw string) (caller, error) {
	if id == nil {
		return caller{}, fmt.Errorf("no identity seam")
	}
	if raw == "" {
		return caller{}, fmt.Errorf("no token")
	}
	t, err := token.Decode(raw, id.secret, true)
	if err != nil {
		return caller{}, err
	}
	if t.Account == "" {
		return caller{}, fmt.Errorf("token has no account")
	}
	user, _ := t.Extra["user"].(string)
	return caller{
		account:   t.Account,
		org:       t.Org(),
		orgs:      orgsFromExtra(t.Extra),
		user:      user,
		workspace: t.Workspace,
		raw:       raw,
	}, nil
}

// admit authorizes cl for the workspace the REQUEST named and returns its row.
// Membership IS the authorization — the server reads the rows, the caller signs
// nothing — which is why it is the one gate both lanes pass through wherever a
// workspace is named. Every failure answers the same errNoWorkspace, so an unknown
// workspace, another tenant's, and one the caller is not in are indistinguishable.
func (id *identity) admit(ctx context.Context, cl caller, wsUUID string) (workspace, error) {
	if id == nil || id.accounts == nil {
		return workspace{}, errNoWorkspace
	}
	// A caller with no tenant, or none with an account, names nothing to be a member
	// of — and an empty org is a value the owner_org scoping would happily match a
	// row against. Refused here, once, so every surface inherits the same floor.
	if cl.org == "" || cl.account == "" {
		return workspace{}, errNoWorkspace
	}
	w, err := id.accounts.WorkspaceByUUID(ctx, cl.org, strings.TrimSpace(wsUUID))
	if err != nil {
		return workspace{}, errNoWorkspace
	}
	if _, ok := id.accounts.Membership(ctx, w.ID, cl.account); !ok {
		return workspace{}, errNoWorkspace
	}
	return w, nil
}

// ── helpers ───────────────────────────────────────────────────────────────────

// account resolves (AccountUuid, org, token) from the request's verified caller.
// The org is the IAM tenant — the HOME org of a verified access token, or the HS256
// token's SIGNED extra.org claim — and is the key for every account-store query,
// never a client header.
//
// The token is EMPTY for an IAM caller, and that is the answer rather than a gap:
// it is echoed to the SPA as its session token, and an IAM caller's credential is
// an estate-wide bearer held in an HttpOnly cookie that script must never see (see
// caller.raw). Such a caller already holds the credential it authenticated with, so
// there is nothing it needs handed back.
func (g *api) account(c *zip.Ctx) (account, org, tok string, err error) {
	cl, err := g.ident.who(c)
	if err != nil {
		return "", "", "", err
	}
	return cl.account, cl.org, cl.raw, nil
}

// accountOrgs resolves (AccountUuid, membership set, token) from the verified
// caller. The set is home-safe — the verified `orgs` claim on the IAM lane, the
// SIGNED extra.orgs on the HS256 one — and is the tenant SET the cross-org surfaces
// (getUserWorkspaces union, selectWorkspace resolution) enumerate. Never a client
// header. Empty account fails closed, exactly like account().
func (g *api) accountOrgs(c *zip.Ctx) (account string, orgs []model.OrgRef, tok string, err error) {
	cl, err := g.ident.who(c)
	if err != nil {
		return "", nil, "", err
	}
	return cl.account, cl.orgs, cl.raw, nil
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
