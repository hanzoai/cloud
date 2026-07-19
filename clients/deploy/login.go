// login.go — the sign-in round trip for the deploy plane at cd.hanzo.ai.
//
// THE PROBLEM. Every /v1/deploy route is SuperAdmin-gated on c.IsAdmin(), which
// SanitizeIdentity mints ONLY from a validated IAM principal whose org IS the
// reserved admin org. The dashboard SPA is served at cd.hanzo.ai/ and calls this
// plane same-origin — but the IAM session cookie is minted host-only on hanzo.id,
// so a session established at hanzo.id or admin.hanzo.ai is never presented to
// cd.hanzo.ai. With no sign-in of its own the whole surface 403s and there is no
// way in. This file IS the way in.
//
//	GET /v1/deploy/login    — start: redirect into IAM's authorize endpoint
//	GET /v1/deploy/callback — finish: exchange the code, mint the session cookie
//	GET /v1/deploy/logout   — clear the session cookie
//
// WHAT IT MINTS — NOT A SECOND SESSION MECHANISM. The callback stores the IAM
// access-token JWT in the `hanzo_iam_token` cookie: the FIRST name in cloud's
// cookieTokenNames, which SanitizeIdentity already reads, independently verifies
// (signature/issuer/audience/expiry against the IAM JWKS) and turns into the same
// principal a Bearer would. So this adds exactly one thing — a way to PUT the
// token in the browser for this host. The gate, the validation, and the SuperAdmin
// predicate are untouched; a forged cookie is still just an invalid JWT, and a
// forged X-User-IsAdmin header is still stripped on ingress.
//
// PUBLIC CLIENT, PKCE. The deploy plane holds no client secret: it drives IAM's
// authorization-code flow with PKCE S256 (RFC 7636), which IAM accepts with an
// empty client_secret when the code carries a challenge. A secret is still sent
// when one is configured, for a deployment that registers a confidential client.
//
// CSRF. The `state` is a fresh 256-bit nonce echoed into a short-lived, HttpOnly,
// Secure, SameSite=Lax cookie alongside the PKCE verifier and the return path. The
// callback accepts a code ONLY when the returned state equals the cookie's nonce
// (constant time), so a login-CSRF — an attacker completing THEIR authorization in
// the victim's browser — is refused. The cookie is the only store, so the flow
// survives any replica handling the callback.
package deploy

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

const (
	// loginPath / callbackPath / logoutPath are the three routes of the round trip.
	// /v1/deploy/<resource>, like the rest of the plane — never an /api/ prefix.
	loginPath    = dashPrefix + "/login"
	callbackPath = dashPrefix + "/callback"
	logoutPath   = dashPrefix + "/logout"

	// sessionCookie is cloud's EXISTING session cookie name — cookieTokenNames[0]
	// in middleware_identity.go. Writing it here is what makes SanitizeIdentity
	// resolve a principal on the next request. Do not invent a second name.
	sessionCookie = "hanzo_iam_token"

	// flowCookie carries the one in-flight OAuth round trip (state nonce, PKCE
	// verifier, return path). It is deleted the moment the callback reads it.
	flowCookie = "hanzo_deploy_oauth"

	// flowTTL bounds how long an unfinished sign-in stays resumable.
	flowTTL = 10 * time.Minute

	// sessionFallbackTTL is the session cookie's lifetime when the token carries
	// no readable exp. The cookie outliving the token is harmless (the token is
	// re-validated on every request) but pointless, so keep it short.
	sessionFallbackTTL = 8 * time.Hour

	// defaultClientID is the IAM application whose ORGANIZATION is the admin org,
	// so a sign-in through it resolves users in `admin` — the only org whose
	// members are SuperAdmins. hanzo-cloud is deliberately NOT used: it is owned by
	// admin but its organization is `hanzo`, so it looks admin-org users up in the
	// wrong org and never finds them.
	defaultClientID = "admin-console"
)

// oauth is the sign-in configuration: where IAM is, who we are to it, and which
// org grants SuperAdmin. Resolved once at build time.
type oauth struct {
	issuer       string // IAM origin, e.g. https://hanzo.id
	clientID     string // IAM application client_id (organization == adminOrg)
	clientSecret string // optional; empty ⟹ public client on PKCE alone
	adminOrg     string // the reserved org whose members are SuperAdmins
	publicURL    string // this plane's PUBLIC origin, when the request Host is internal
	http         *http.Client
}

// newOAuth resolves the sign-in configuration from deps + env. The issuer comes
// from the SAME value the identity boundary validates tokens against
// (deps.IAMIssuer), so a token this flow mints is a token cloud accepts.
func newOAuth(deps cloud.Deps) oauth {
	return oauth{
		issuer:       strings.TrimRight(firstNonEmpty(deps.IAMIssuer, os.Getenv("IAM_ENDPOINT"), "https://hanzo.id"), "/"),
		clientID:     firstNonEmpty(os.Getenv("DEPLOY_IAM_CLIENT_ID"), defaultClientID),
		clientSecret: os.Getenv("DEPLOY_IAM_CLIENT_SECRET"),
		adminOrg:     firstNonEmpty(os.Getenv("IAM_ADMIN_ORG"), "admin"),
		publicURL:    strings.TrimRight(firstNonEmpty(os.Getenv("DEPLOY_PUBLIC_URL"), os.Getenv("PUBLIC_ORIGIN")), "/"),
		http:         &http.Client{Timeout: 15 * time.Second},
	}
}

// oauthBase is the canonical IAM OAuth base: ${issuer}/v1/iam. IAM mounts
// authorize/token/userinfo under /v1/iam — never at the root, never under /api/.
func (o oauth) oauthBase() string { return o.issuer + "/v1/iam" }

// callbackOrigin is the origin the redirect_uri is built from. It MUST produce the
// byte-identical string in login (authorize) and callback (token exchange) — IAM
// compares them. Behind the gateway the request Host is the internal cluster host,
// which IAM's redirect allowlist rejects, so a configured public origin wins.
func (o oauth) callbackOrigin(c *zip.Ctx) string {
	if o.publicURL != "" {
		return o.publicURL
	}
	host := strings.TrimSpace(c.Fiber().Host())
	if host == "" {
		host = "localhost"
	}
	scheme := "https"
	if proto := c.Header("X-Forwarded-Proto"); proto != "" {
		scheme = proto
	} else if strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1") {
		scheme = "http"
	}
	return scheme + "://" + host
}

func (o oauth) redirectURI(c *zip.Ctx) string { return o.callbackOrigin(c) + callbackPath }

// ── the round trip ───────────────────────────────────────────────────────────

// flow is the one in-flight sign-in, carried in flowCookie for the duration of the
// external hop. Nonce is echoed as `state`; Verifier is the PKCE secret that is
// never sent to the browser's address bar; Return is the already-validated
// same-host path to land on.
type flow struct {
	Nonce    string `json:"n"`
	Verifier string `json:"v"`
	Return   string `json:"r"`
}

// login starts the round trip: mint a nonce + PKCE verifier, remember them in the
// flow cookie, and send the browser to IAM's authorize endpoint.
func login(s *cloud.Service[state], c *zip.Ctx) error {
	nonce, err := randomToken()
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "login: %v", err)
	}
	verifier, err := randomToken()
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "login: %v", err)
	}
	f := flow{Nonce: nonce, Verifier: verifier, Return: safeReturn(c.Query("returnTo"))}
	blob, err := json.Marshal(f)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "login: %v", err)
	}
	setCookie(c, flowCookie, base64.RawURLEncoding.EncodeToString(blob), int(flowTTL.Seconds()))

	o := s.State.oauth
	q := url.Values{
		"client_id":             {o.clientID},
		"redirect_uri":          {o.redirectURI(c)},
		"response_type":         {"code"},
		"scope":                 {"openid profile email"},
		"state":                 {nonce},
		"code_challenge":        {pkceChallenge(verifier)},
		"code_challenge_method": {"S256"},
	}
	return c.Redirect(http.StatusFound, o.oauthBase()+"/oauth/authorize?"+q.Encode())
}

// callback finishes the round trip: verify the state against the flow cookie,
// exchange the code (PKCE), REFUSE a principal that is not in the admin org, then
// write the session cookie and land on the return path.
//
// FAIL CLOSED, TWICE. The admin-org check here is not the authorization decision —
// SanitizeIdentity re-derives it from the verified JWT on every subsequent request,
// and guard() gates on that. It is here so a non-SuperAdmin is told plainly that
// they lack the role instead of being handed a session that silently 403s
// everything, and so no cookie is ever minted for a principal the plane will refuse.
func callback(s *cloud.Service[state], c *zip.Ctx) error {
	raw := c.Fiber().Cookies(flowCookie)
	clearCookie(c, flowCookie) // single use: consumed whether or not it validates
	f, err := decodeFlow(raw)
	if err != nil {
		return zip.ErrBadRequest("no sign-in is in progress; start at " + loginPath)
	}
	// CSRF: the code is only accepted for the round trip THIS browser started.
	if subtle.ConstantTimeCompare([]byte(f.Nonce), []byte(c.Query("state"))) != 1 {
		return zip.ErrBadRequest("state mismatch; start again at " + loginPath)
	}
	if e := c.Query("error"); e != "" {
		s.Log.Warn("deploy sign-in refused by IAM", "error", e)
		return zip.ErrUnauthorized("sign-in was not completed")
	}
	code := c.Query("code")
	if code == "" {
		return zip.ErrBadRequest("missing authorization code")
	}

	access, err := s.State.oauth.exchange(c.Context(), code, s.State.oauth.redirectURI(c), f.Verifier)
	if err != nil {
		s.Log.Error("deploy sign-in code exchange failed", "err", err)
		return zip.ErrUnauthorized("sign-in could not be completed")
	}
	owner, user := tokenIdentity(access)
	if owner != s.State.oauth.adminOrg {
		s.Log.Warn("deploy sign-in refused: not a SuperAdmin", "user", user, "org", owner)
		return zip.ErrForbidden("SuperAdmin required: this console is limited to members of the " +
			s.State.oauth.adminOrg + " organization")
	}

	setCookie(c, sessionCookie, access, sessionMaxAge(access))
	s.Log.Info("deploy sign-in", "user", user, "org", owner)
	return c.Redirect(http.StatusFound, f.Return)
}

// logout clears the session cookie for this host. IAM's own session is untouched —
// this ends the cd.hanzo.ai session only.
func logout(s *cloud.Service[state], c *zip.Ctx) error {
	clearCookie(c, sessionCookie)
	return c.Redirect(http.StatusFound, "/")
}

// exchange redeems the authorization code at IAM's token endpoint with the PKCE
// verifier. The client secret is sent only when one is configured (IAM accepts an
// empty secret for a code that carries a challenge).
func (o oauth) exchange(ctx context.Context, code, redirect, verifier string) (string, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirect},
		"client_id":     {o.clientID},
		"code_verifier": {verifier},
	}
	if o.clientSecret != "" {
		form.Set("client_secret", o.clientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.oauthBase()+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := o.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("token endpoint: %w", err)
	}
	defer resp.Body.Close()
	// Bound the read: a token response is small, and this body is attacker-
	// influenced only insofar as IAM is reachable — cap it regardless.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("token endpoint status %d", resp.StatusCode)
	}
	var out struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("token response: %w", err)
	}
	if out.Error != "" {
		return "", fmt.Errorf("token endpoint: %s", out.Error)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("token endpoint returned no access_token")
	}
	return out.AccessToken, nil
}

// ── pure helpers (unit-tested) ───────────────────────────────────────────────

// safeReturn constrains the post-sign-in landing spot to a path on THIS host: it
// must be absolute-rooted, must not be protocol-relative ("//evil" or "/\evil",
// which browsers resolve as another origin), and must carry no scheme or host.
// Anything else collapses to "/". This is the open-redirect guard.
func safeReturn(raw string) string {
	if raw == "" || raw[0] != '/' {
		return "/"
	}
	if len(raw) > 1 && (raw[1] == '/' || raw[1] == '\\') {
		return "/"
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "/"
	}
	return u.RequestURI()
}

// wantsDocument reports whether a refused request is a BROWSER NAVIGATION, which
// should be bounced to the sign-in page rather than handed a 403 the user cannot
// act on. Everything else — every XHR, every API client, every request that does
// not positively identify as a document GET — keeps the 403, so the API contract
// is unchanged.
//
// It is deliberately conservative and ordered by trustworthiness: Sec-Fetch-Dest /
// Sec-Fetch-Mode are set by the browser and cannot be forged from page JS, so when
// present they DECIDE. Only when both are absent does it fall back to Accept.
// A non-GET is never redirected: bouncing a POST would silently drop a mutation.
func wantsDocument(method, dest, mode, accept, requestedWith string) bool {
	if method != http.MethodGet {
		return false
	}
	if dest != "" {
		return dest == "document"
	}
	if mode != "" {
		return mode == "navigate"
	}
	if strings.EqualFold(requestedWith, "XMLHttpRequest") {
		return false
	}
	if strings.Contains(accept, "application/json") {
		return false
	}
	return strings.Contains(accept, "text/html")
}

// pkceChallenge is the RFC 7636 S256 challenge: base64url(sha256(verifier)),
// unpadded — byte-identical to IAM's own pkceChallenge.
func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// randomToken mints a 256-bit URL-safe secret (the state nonce and the PKCE
// verifier). A failure of the system CSPRNG fails the sign-in — never a weaker one.
func randomToken() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("entropy unavailable: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

// decodeFlow parses the flow cookie. A missing, malformed, or field-empty cookie is
// an error — the callback then has nothing to compare `state` against and refuses.
func decodeFlow(raw string) (flow, error) {
	var f flow
	if raw == "" {
		return f, fmt.Errorf("no flow cookie")
	}
	blob, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return f, fmt.Errorf("flow cookie: %w", err)
	}
	if err := json.Unmarshal(blob, &f); err != nil {
		return f, fmt.Errorf("flow cookie: %w", err)
	}
	if f.Nonce == "" || f.Verifier == "" {
		return f, fmt.Errorf("flow cookie is incomplete")
	}
	// Re-validate on the way out: the return path is re-checked against the same
	// open-redirect rule that admitted it, so a tampered cookie cannot bounce the
	// browser off-host.
	f.Return = safeReturn(f.Return)
	return f, nil
}

// tokenIdentity reads the `owner` (org) and `name` (username) claims from a JWT
// WITHOUT verifying it. That is sound here and nowhere else: the token came back
// over TLS from IAM's own token endpoint in a server-to-server call, so its
// authenticity is established by transport, and nothing is authorized on the basis
// of what this returns — SanitizeIdentity independently verifies signature, issuer,
// audience and expiry on every later request, and guard() gates on THAT. These
// claims only decide whether it is worth minting a cookie at all, and what to log.
func tokenIdentity(access string) (owner, user string) {
	parts := strings.Split(access, ".")
	if len(parts) != 3 {
		return "", ""
	}
	blob, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", ""
	}
	var claims struct {
		Owner string `json:"owner"`
		Name  string `json:"name"`
		Sub   string `json:"sub"`
	}
	if err := json.Unmarshal(blob, &claims); err != nil {
		return "", ""
	}
	return claims.Owner, firstNonEmpty(claims.Name, claims.Sub)
}

// sessionMaxAge is the session cookie's lifetime: the token's own remaining life,
// so the cookie dies with the credential it carries.
func sessionMaxAge(access string) int {
	parts := strings.Split(access, ".")
	if len(parts) != 3 {
		return int(sessionFallbackTTL.Seconds())
	}
	blob, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return int(sessionFallbackTTL.Seconds())
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(blob, &claims); err != nil || claims.Exp == 0 {
		return int(sessionFallbackTTL.Seconds())
	}
	if secs := int(time.Until(time.Unix(claims.Exp, 0)).Seconds()); secs > 0 {
		return secs
	}
	return int(sessionFallbackTTL.Seconds())
}

// ── cookies ──────────────────────────────────────────────────────────────────

// setCookie writes a host-only, HttpOnly, Secure, SameSite=Lax cookie.
//
// HttpOnly: page JS never touches the session token. Secure: it never rides plain
// HTTP. Lax (not Strict): the sign-in lands here via a top-level redirect FROM
// hanzo.id, and Strict would withhold the cookie on that first cross-site
// navigation, so the user would arrive still signed out. Lax is the correct
// setting for an OAuth round trip and still withholds the cookie from cross-site
// subrequests. No Domain attribute: the cookie stays host-only to this console and
// is never broadcast to sibling *.hanzo.ai hosts.
func setCookie(c *zip.Ctx, name, value string, maxAge int) {
	c.Fiber().Res().Cookie(&fiber.Cookie{
		Name: name, Value: value, Path: "/",
		HTTPOnly: true, Secure: true, SameSite: fiber.CookieSameSiteLaxMode,
		MaxAge: maxAge,
	})
}

func clearCookie(c *zip.Ctx, name string) { setCookie(c, name, "", -1) }

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
