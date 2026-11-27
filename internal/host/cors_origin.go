package host

// The CORS origin predicate: may this browser origin read an answer from this
// edge, and may it read it with the caller's cookies attached?
//
// Every web origin may read. The API is called from customers' own sites with
// their own keys, and a key travels in the request the page builds, never in the
// visitor's cookie jar, so admitting an origin to read WITHOUT credentials lets
// it do nothing a script on any server cannot already do. What an origin cannot
// get is credentials: Access-Control-Allow-Credentials, the power to spend the
// visitor's Hanzo session, is first-party only.
//
//	CREDENTIALED — an origin the platform allowlist DECLARES
//	               (edge.Policy.CORSOrigins, SuperAdmin-writable), or a host the
//	               brand registry OWNS (brand.Owned) over https on the default port.
//	ANONYMOUS    — every other well-formed http(s) origin, a customer's domain,
//	               a site published under hanzo.app and http://localhost alike.
//	REFUSED      — `null` and anything that is not exactly scheme://host[:port],
//	               so the string echoed back is always its own canonical form.
//
// Credentials follow the class and nothing else, here and nowhere downstream;
// the sign-in routes are narrower still (signIn, below).

import (
	"context"
	"net/url"
	"path"
	"strings"
	"sync/atomic"

	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/sdk/fqdn"
)

// corsOrigins answers the one question over the declared allowlist and the brand
// registry.
//
// declared is ATOMIC because this value has two readers on different goroutines —
// the edge middleware and, through CORSAllows, the ai filter — while the middleware
// recompiles it whenever an operator retunes the live allowlist. A plain field
// would be a write racing two reads on every policy change.
type corsOrigins struct {
	declared atomic.Pointer[originMatcher]
}

// setDeclared installs a freshly compiled allowlist.
func (o *corsOrigins) setDeclared(m *originMatcher) { o.declared.Store(m) }

// corsClass is the verdict for an origin: refused, admitted to read anonymously,
// or admitted to read with the caller's credentials.
type corsClass int

const (
	corsRefused corsClass = iota
	corsAnonymous
	corsCredentialed
)

// allowed reports whether the origin is admitted at all. The preflight and the
// actual request are decided by the same admit call, so they cannot disagree.
func (o *corsOrigins) allowed(ctx context.Context, origin string) bool {
	return o.admit(ctx, origin) != corsRefused
}

// admit is the predicate. It reads no store: an Origin header is chosen by the
// caller and arrives before authentication, so nothing it names may cost a lookup.
func (o *corsOrigins) admit(_ context.Context, origin string) corsClass {
	// An origin the operator DECLARED is credentialed on its own terms, including
	// the wildcard and bare-host forms that allowlist has always used.
	if d := o.declared.Load(); d != nil && d.allowed(origin) {
		return corsCredentialed
	}
	if !webOrigin(origin) {
		return corsRefused
	}
	if host, ok := secureHost(origin); ok && brand.Owned(host) {
		return corsCredentialed
	}
	return corsAnonymous
}

// webOrigin reports whether an Origin header is a browser origin this edge may
// echo back: exactly `http(s)://host[:port]`, reconstructed and compared, so a
// path, a query, a fragment, userinfo, a trailing slash, an upper-case scheme and
// (through url.Parse, which refuses them) any control character are refused. The
// echoed value therefore always equals its own canonical serialization, and no
// caller can fold a header into the response. `null` (a sandboxed frame, a file)
// is refused: it names no site.
func webOrigin(origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return false
	}
	return origin == u.Scheme+"://"+u.Host
}

// secureHost reduces an Origin to its host when the origin may carry credentials,
// and reports false otherwise:
//
//   - https only. Access-Control-Allow-Credentials over cleartext puts the
//     credential on the wire.
//   - no port. `https://hanzo.ai:8443` is a different origin, and admitting it
//     widens the credentialed set for no product gain.
//   - fqdn.Valid, already canonical: no IP literal, no `localhost`, no bare label,
//     no upper case or trailing root dot folded onto a name it does not spell.
func secureHost(origin string) (string, bool) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", false
	}
	if origin != u.Scheme+"://"+u.Host || u.Port() != "" {
		return "", false
	}
	host := u.Hostname()
	if host != fqdn.Clean(host) || !fqdn.Valid(host) {
		return "", false
	}
	return host, true
}

// originMatcher decides whether a request Origin is on the DECLARED allowlist. Each
// config entry is either an EXACT origin ("https://hanzo.ai") or a host wildcard
// ("*.hanzo.ai", which matches the apex `hanzo.ai` AND any subdomain
// `<sub>.hanzo.ai`) — the two forms the gateway/ingress allowlists use, expressed
// once. Bare-host entries ("hanzo.ai") match that host. Host and wildcard entries
// admit only https:// on the default port; anything else must be an exact origin.
type originMatcher struct {
	exact  map[string]struct{} // full origin strings, e.g. "https://hanzo.ai"
	hosts  map[string]struct{} // bare hosts matched regardless of scheme
	suffix []string            // wildcard hosts: apex value, e.g. "hanzo.ai"
}

// newOriginMatcher compiles the allowlist. Returns nil when empty, so the caller
// can tell "the operator declared nothing" from "the operator declared nothing that
// matched".
func newOriginMatcher(origins []string) *originMatcher {
	if len(origins) == 0 {
		return nil
	}
	m := &originMatcher{exact: map[string]struct{}{}, hosts: map[string]struct{}{}}
	for _, o := range origins {
		o = strings.TrimSpace(o)
		if o == "" {
			continue
		}
		switch {
		case strings.HasPrefix(o, "*."):
			m.suffix = append(m.suffix, strings.ToLower(o[2:]))
		case strings.Contains(o, "://"):
			m.exact[o] = struct{}{}
		default:
			m.hosts[strings.ToLower(o)] = struct{}{}
		}
	}
	if len(m.exact) == 0 && len(m.hosts) == 0 && len(m.suffix) == 0 {
		return nil
	}
	return m
}

func (m *originMatcher) allowed(origin string) bool {
	if _, ok := m.exact[origin]; ok {
		return true
	}
	// A host or wildcard entry names HTTPS on the default port. The response it
	// earns carries credentials, and an http:// origin (or one on another port) is
	// a different origin that anything on the path can impersonate. An entry that
	// means plain http, such as a local dev server, is written as an exact origin.
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Port() != "" {
		return false
	}
	if origin != u.Scheme+"://"+u.Host {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if _, ok := m.hosts[host]; ok {
		return true
	}
	for _, sfx := range m.suffix {
		if host == sfx || strings.HasSuffix(host, "."+sfx) {
			return true
		}
	}
	return false
}

// ── sign-in routes ──────────────────────────────────────────────────────────

// The IAM routes that set the identity provider's session, or spend it to mint a
// grant, are a different question from the rest of the API. A credentialed
// answer on them is the power to sign a person in, and the session cookie rides
// on every request from a page same-site with the identity host. That power
// belongs to the page a person signs in on — an IDENTITY origin
// (Config.IdentityHosts) — and not to every page the allowlist admits.
//
// An app sends people to its brand's identity origin with PKCE and redeems the
// code at the token endpoint, which proves itself in the request, carries no
// cookie, and is untouched here.

// signIn is the set of identity hosts.
type signIn map[string]bool

// newSignIn compiles the identity hosts, keeping only well-formed names so a
// malformed entry can match nothing.
func newSignIn(hosts []string) signIn {
	s := signIn{}
	for _, h := range hosts {
		if h = fqdn.Clean(h); fqdn.Valid(h) {
			s[h] = true
		}
	}
	return s
}

// has reports whether origin is exactly https://<identity host>. It applies the
// credentialed rule (secureHost): https only, no port, nothing after the host,
// already canonical — so a near-miss spelling is a miss.
func (s signIn) has(origin string) bool {
	h, ok := secureHost(origin)
	return ok && s[h]
}

// sessionRoutes are the IAM addresses that set the session cookie, spend it to
// mint a grant, or take a sign-in credential (hanzoai/iam): login (sessions.Open,
// the SSO code, and device approval), signin (the code-to-session exchange),
// signup (a new account's password), onboard (re-keys the session to the new
// org), accounts (everyone signed in on the browser, read from the cookie alone),
// authorize (the session answers prompt=none with a code), the device flow's
// approval page, the federation callback and its second factor, and the wallet
// and passkey ceremonies — each leg of which ends in a sign-in. An app gets no
// CORS answer on any of them.
//
// A whole-segment prefix: "/v1/iam/web3" covers /v1/iam/web3/nonce and
// /v1/iam/web3/verify, and does not cover /v1/iam/webauthn-credentials. The
// token, userinfo, discovery, JWKS, revoke and logout routes are NOT here: a
// PKCE client calls them from its own origin and proves itself in the request,
// never with the cookie.
var sessionRoutes = []string{
	"/v1/iam/login",
	"/v1/iam/signin",
	"/v1/iam/signup",
	"/v1/iam/onboard",
	"/v1/iam/accounts",
	"/v1/iam/oauth/authorize",
	"/v1/iam/oauth/device",
	"/v1/iam/oauth/callback",
	"/v1/iam/oauth/federation/mfa",
	"/v1/iam/web3",
	"/v1/iam/webauthn",
}

// cookieRoutes are the IAM addresses that resolve their caller from the session
// cookie FIRST and a bearer second (hanzoai/iam callerOf): the account, whoami,
// consent, preferences, password, and the link, unlink and linked-accounts
// routes. An app calls them with its bearer and keeps that answer; only an
// identity origin gets the credential, so no other page spends the session here.
var cookieRoutes = []string{
	"/v1/iam/account",
	"/v1/iam/whoami",
	"/v1/iam/consent",
	"/v1/iam/preferences",
	"/v1/iam/password",
	"/v1/iam/link",
	"/v1/iam/unlink",
	"/v1/iam/linked-accounts",
}

// sessionRoute reports whether p addresses one of sessionRoutes.
func sessionRoute(p string) bool { return addresses(p, sessionRoutes) }

// cookieRoute reports whether p addresses one of cookieRoutes.
func cookieRoute(p string) bool { return addresses(p, cookieRoutes) }

// addresses reports whether p names one of routes, in every spelling that can
// arrive at it. The router matches without regard to case and ignores a trailing
// slash (fiber's defaults), and a relay that re-sends the request normalizes the
// path on the way (fasthttp's client unescapes it and collapses // . and ..). So
// the path is judged as the handler will finally see it: unescaped until it stops
// changing, cleaned, lower-cased. Over-matching only withholds an answer.
func addresses(p string, routes []string) bool {
	for range 4 {
		u, err := url.PathUnescape(p)
		if err != nil || u == p {
			break
		}
		p = u
	}
	p = strings.ToLower(path.Clean("/" + p))
	for _, r := range routes {
		if p == r || strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}
