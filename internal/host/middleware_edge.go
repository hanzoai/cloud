package host

// Edge policy middleware — the in-process "gateway role" cloud absorbs so it can
// serve the public api.hanzo.ai edge DIRECTLY, with no separate KrakenD gateway
// hop (hanzoai/gateway). It is two ORTHOGONAL concerns, each a distinct slot:
//
//	EdgeCORS      — browser CORS at the /v1 edge (the gateway routes.go role).
//	EdgeRateLimit — per-client-IP flood cap BEFORE identity (the gateway
//	                qos/ratelimit/router role).
//
// Both read their policy LIVE from the edge.Store (the /v1/gateway
// runtime config plane) on every request, so an operator can retune CORS origins
// or the per-IP cap via PUT /v1/gateway/config with no redeploy. The store layers
// the admin-org "platform" policy over the static boot defaults, so an
// un-provisioned deployment behaves exactly as the env/flag config until a policy
// is written. See clients/gateway/edge + clients/gateway.
//
// These are DELIBERATELY not the things cloud already does. The gateway's other
// jobs are already owned in-binary and are NOT re-implemented here:
//
//   - JWT validate + strip/re-mint identity headers  → SanitizeIdentity
//     (middleware_identity.go / auth_identity.go). The gateway's auth/validator
//     is redundant with it; EdgeCORS/EdgeRateLimit run AROUND it, never re-do it.
//   - Authenticated per-org rate ceiling            → ScopeRateLimit
//     (middleware_ratelimit.go), keyed on the VALIDATED principal, now also
//     honoring the /v1/gateway per-org OrgRPM.
//   - Balance / spend-cap quota                        → BillingGate.
//
// EdgeRateLimit fills the ONE gap those post-auth gates leave: an ANONYMOUS flood
// (no valid JWT ⇒ no org to key on) is invisible to ScopeRateLimit, which keys on
// the validated org. It must be throttled by client IP at the very edge —
// before the JWKS fetch / signature verify / downstream work the request would
// otherwise trigger — and a credential that validates to nobody is that same
// flood, so the boundary finishes the charge for it (nobody). A caller
// who IS somebody is ScopeRateLimit's, not this.

import (
	"context"
	"github.com/hanzoai/cloud/sdk"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/hanzoai/ai/address"
	"github.com/valyala/fasthttp"
	"github.com/zap-proto/zip"
)

// ─────────────────────────────────────────────────────────────────────────────
// CORS
// ─────────────────────────────────────────────────────────────────────────────

// corsAllowMethods / corsMaxAge state the rest of the browser contract.
// Credentialed reflect-Origin: the allowlisted request Origin is echoed verbatim
// (never `*` with credentials), so a cookie/Authorization request from an allowed
// brand host works and every other origin gets no CORS headers.
const (
	corsAllowMethods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
	corsMaxAge       = "86400"
)

// corsExposeHeaders are the response headers a script in an admitted page may
// read beyond the CORS-safelisted few (Fetch, "CORS-safelisted response-header
// name"): the ones a 401 and a 429 carry to say what to do next, the two an x402
// answer carries — the terms a 402 asks the page to sign, and the receipt a paid
// answer returns — and the usage policy's word on a model call: where the class
// stands, which class it was, who paid, the model that answered in its place and
// why, and for a plan that used its usage when it reopens and where to buy credits
// or a bigger plan; and where a model caller stands against the requests a minute
// one person may make, in OpenAI's names (apps/ai rate.go), which a client paces
// itself by. Without them a browser SDK sees the status and nothing it can act on —
// not when to retry, not which credential to send, not what to pay, not that a reply
// came from a free model.
const corsExposeHeaders = "Retry-After, WWW-Authenticate, X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset, PAYMENT-REQUIRED, PAYMENT-RESPONSE, " +
	"X-Hanzo-Usage, X-Hanzo-Usage-Class, X-Hanzo-Paid-By, X-Hanzo-Fallback, X-Hanzo-Usage-Reason, X-Hanzo-Usage-Resets, X-Hanzo-Topup-Url, X-Hanzo-Upgrade-Url, " +
	"x-ratelimit-limit-requests, x-ratelimit-remaining-requests, x-ratelimit-reset-requests"

// corsAllowHeaders answers a preflight with the header names the BROWSER said the
// script attached — the Access-Control-Request-Headers it is asking about.
//
// This used to be a literal list, and a literal list is a standing outage waiting
// for the next client header. A name absent from it fails PREFLIGHT, and the
// browser reports that as an opaque "TypeError: Failed to fetch" with nothing at
// all on the server side to see — the request never arrives, so there is no log,
// no metric and no 4xx to notice. It has now cost us twice: first the console's
// X-Actor-Id / X-Act-As-Project / X-Act-As-Org / X-CSRF-Token, then
// billing.hanzo.ai's X-Idempotency-Key, whose absence meant NO paid subscription
// POST could leave the browser — the Square nonce was minted and the request died
// at the preflight. Echoing the ask is the Fetch-standard way to say "all of
// them", and it is the only answer that cannot go stale.
//
// Answering the ask widens nothing. Naming a header only lets the browser SEND
// it; each stays exactly as trustworthy as before, because SanitizeIdentity still
// strips and re-mints every client-supplied identity header — an intent is
// validated, never believed. The forbidden names a script must never set (Cookie,
// Host, Origin…) the browser refuses on its own, whatever we allow. The origin
// check above is the enforcement, and it did not move.
//
// Only well-formed tokens are echoed, so a hand-written request cannot fold a CRLF
// into a response header. A malformed name is dropped rather than answered; the
// browser then blocks the call, which is the correct answer to an ask we did not
// grant.
func corsAllowHeaders(ask string) string {
	names := make([]string, 0, 8)
	for name := range strings.SplitSeq(ask, ",") {
		if name = strings.TrimSpace(name); headerToken(name) {
			names = append(names, name)
		}
	}
	return strings.Join(names, ", ")
}

// headerToken reports whether s is a single RFC 9110 field-name token — the only
// shape a header name can have.
func headerToken(s string) bool {
	const tchar = "!#$%&'*+-.^_`|~0123456789" +
		"abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	return s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !strings.ContainsRune(tchar, r)
	}) < 0
}

// EdgeCORS returns the browser-CORS middleware for the public /v1 edge, and it is
// THE CORS authority for this binary: one predicate decides the preflight and the
// actual request, and nothing downstream re-decides.
//
// Every web origin may read the API (cors_origin.go): customers call it from their
// own sites with their own keys, and a key is in the request the page builds, never
// in the visitor's cookie jar. Credentials are the narrow thing. An origin the
// PLATFORM policy's CORSOrigins declares (read live, so a SuperAdmin retunes it via
// PUT /v1/gateway/config) or a host the brand registry owns gets
// Access-Control-Allow-Credentials; every other origin reads without them.
//
// Two IAM route classes are narrower, and both turn on the identity origins
// (identity, normally Config.IdentityHosts). The routes that set or spend the
// sign-in session (sessionRoute) answer only an identity origin. The routes that
// read the session cookie ahead of a bearer (cookieRoute) answer an admitted app
// without the credential, which is all its bearer needs. Everywhere else an
// admitted first-party app keeps its credentialed answer. The narrowing only
// withholds. Nothing in front of this layer repeats that rule: Cloudflare and the
// ingress pass the answer through untouched.
//
// Preflights answer with the headers the browser asked for and are cached by the
// browser for corsMaxAge. The preflight is short-circuited (204) and the actual
// response reflects the origin. Both hang off the SAME verdict, so there is no
// arrangement in which a browser is told yes at the preflight and no at the
// request.
func EdgeCORS(pol *edge.Store, identity []string) zip.Handler {
	var (
		mu      sync.Mutex
		lastKey string
		origins = &corsOrigins{}
		signsIn = newSignIn(identity)
	)
	// Publish the instance, not a copy: apps/ai asks THIS predicate, sharing its
	// compiled allowlist and its cache, so the two layers cannot drift.
	corsPredicate.Store(origins)
	// current recompiles the DECLARED matcher only when the live allowlist string
	// changes; the list is small and Platform() is itself TTL-cached, so this stays
	// cheap.
	current := func() *corsOrigins {
		list := pol.Platform().CORSOrigins
		key := strings.Join(list, "\n")
		mu.Lock()
		defer mu.Unlock()
		if key != lastKey {
			lastKey = key
			origins.setDeclared(newOriginMatcher(list))
		}
		return origins
	}
	return func(c *zip.Ctx) error {
		if shared(c, pol) {
			// An anonymous read of a catalog path is the same answer for every
			// origin, so it says so: `*`, no credentials, no Vary: Origin, and one
			// copy at the edge serves every page that asks. The SAME answer with no
			// Origin at all, because the edge keys on the URL alone and the first
			// request to fill its copy may be a crawler's: a copy without the header
			// would be handed to the next browser and refused there. The Origin is
			// taken off the request for the reason EdgeOrigin takes an admitted one
			// off: no child may answer it a second time.
			c.SetHeader("Access-Control-Allow-Origin", "*")
			c.SetHeader("Access-Control-Expose-Headers", corsExposeHeaders)
			c.Fiber().Request().Header.Del("Origin")
			return c.Continue()
		}
		origin := c.Header("Origin")
		if origin == "" {
			// Not a cross-origin browser request. Nothing here depends on Origin, so
			// nothing is added — not even Vary.
			return c.Continue()
		}
		// EVERY answer below depends on Origin, INCLUDING the one that carries no
		// CORS header, so the cache key must say so or a shared cache will hand one
		// origin the response computed for another. Set before the branch, and
		// APPENDED rather than assigned: SetHeader("Vary", …) overwrites, which is
		// how this used to fight middleware_markdown's Vary: Accept — last writer
		// won and one of the two protections silently vanished. Fiber's Vary appends
		// and is idempotent.
		c.Fiber().Vary("Origin")

		// A refused origin (`null`, or a string that is not exactly an origin) gets
		// no CORS headers and the browser blocks the read. The REQUEST is not
		// refused: this middleware is in front of every route, and a 403 would break
		// the many non-browser callers that send a stray Origin. Withholding the
		// header is the whole enforcement — it is what the browser acts on.
		//
		// Admitted: reflect the exact origin. Credentialed CORS is NEVER wildcard —
		// `*` with Access-Control-Allow-Credentials is invalid per the Fetch
		// standard, and the value echoed here is already its own canonical form
		// (webOrigin).
		signsInHere := signsIn.has(origin)
		class := current().admit(c.Context(), origin)
		// A sign-in route sets or spends the session cookie, so an answer without
		// the credential is no answer at all: it is given only to an identity origin
		// that is also first-party.
		allowed := class != corsRefused && (!sessionRoute(c.Path()) || (signsInHere && class == corsCredentialed))
		if allowed {
			c.SetHeader("Access-Control-Allow-Origin", origin)
			if class == corsCredentialed && (signsInHere || !cookieRoute(c.Path())) {
				c.SetHeader("Access-Control-Allow-Credentials", "true")
			}
			c.SetHeader("Access-Control-Expose-Headers", corsExposeHeaders)
		}
		// A PREFLIGHT is defined by the header that makes it one (Fetch, CORS
		// preflight request): an OPTIONS carrying Access-Control-Request-Method.
		// A BARE OPTIONS is a different question — RFC 9110 §9.3.7, "what does this
		// resource accept" — and answering it as a preflight told every non-browser
		// caller 204 with no Allow, which is the one field the question is about.
		// So this owns the preflight and only the preflight; the bare one carries on
		// to the contract's own endpoint (openapi.UseIndex), which knows the methods
		// because it holds the document.
		if preflight(c) {
			// This middleware OWNS the preflight: it is the only thing in the process
			// that answers one, so it answers every one, and admission decides which
			// headers ride along rather than whether there is a reply at all. A
			// preflight continuing past here meets a router with no OPTIONS route and
			// is told the address does not exist — the least true answer available, and
			// one that hides the missing header the browser actually stopped on.
			if allowed {
				// The allow-headers answer depends on what the browser asked, so the
				// cache key has to say so for the same reason Vary: Origin does.
				c.Fiber().Vary("Access-Control-Request-Headers")
				c.SetHeader("Access-Control-Allow-Methods", corsAllowMethods)
				c.SetHeader("Access-Control-Allow-Headers", corsAllowHeaders(c.Header("Access-Control-Request-Headers")))
				c.SetHeader("Access-Control-Max-Age", corsMaxAge)
			}
			// Short-circuit the preflight: 204, no body, no auth/rate work.
			c.Status(204)
			return nil
		}
		return c.Continue()
	}
}

// shared reports whether a request reads a catalog path anonymously: a GET or
// HEAD carrying no credential and asking for no socket, on a path the platform
// policy lists in cache_paths (publicCatalog). Its answer was computed for nobody,
// so it is every origin's. A credential makes the answer its caller's, and that
// one keeps the reflected origin and its credentials.
func shared(c *zip.Ctx, pol *edge.Store) bool {
	if m := c.Method(); m != http.MethodGet && m != http.MethodHead {
		return false
	}
	if c.Header("Upgrade") != "" {
		return false
	}
	for _, h := range credentialHeaders {
		if c.Header(h) != "" {
			return false
		}
	}
	return pol.Catalog(c.Path())
}

// corsPredicate is the process's ONE compiled CORS origin predicate — the instance
// EdgeCORS built, published so the other layer that would otherwise take its own
// CORS decision can ask THIS one instead.
//
// That layer is hanzoai/ai. It is mounted in-process and registers a CORS filter
// ahead of every /v1 route (routers/filters.go), which REFUSES with 403 any origin
// outside a 21-domain suffix list compiled into that module — a list no customer
// domain can ever be in, and no deployment can change. Two CORS authorities on one
// request is the defect: without this, a customer's console passes the preflight
// here and gets 403 on the actual call there, which is precisely the failure mode
// CORS review exists to catch.
//
// Published as a FUNCTION over the shared instance rather than a per-request mark:
// a mark travels as a request local or a header, and a header is forgeable by the
// caller — it would let a client assert its own CORS verdict. Both layers calling
// one function cannot disagree, and there is nothing on the wire to forge.
var corsPredicate atomic.Pointer[corsOrigins]

// CORSAllows reports whether this deployment's CORS allowlist admits origin.
//
// It is the ORIGIN verdict and ignores the route. EdgeCORS narrows the sign-in
// routes further, and EdgeOrigin must not follow it there: it strips on this
// verdict so that an origin EdgeCORS answered with nothing reaches no child that
// could answer it instead.
//
// Exported for apps/ai, which uses it to keep the mounted ai module's own filter
// inert. Answers false before the edge is built (no app, no policy, nothing
// admitted), which is the fail-secure direction.
func CORSAllows(ctx context.Context, origin string) bool {
	o := corsPredicate.Load()
	if o == nil {
		return false
	}
	return o.allowed(ctx, origin)
}

// EdgeOrigin makes EdgeCORS's verdict the only one. EdgeCORS is the CORS
// authority for this binary; a child that also inspects Origin would answer the
// same question a second time, and two Access-Control-Allow-Origin headers break
// every preflight that reads them. So for an origin the allowlist admits, the
// header is taken off the request before any child sees it — a child with no
// Origin to read cannot form an opinion about one.
//
// It strips on the ALLOWLIST verdict (CORSAllows), not on what EdgeCORS answered,
// and the difference is load-bearing on the sign-in routes. There an app origin
// gets nothing from EdgeCORS, and the embedded IAM runs its own filter, which
// grants Access-Control-Allow-Credentials to every console in
// IAM_SESSION_ORIGINS. Left the header, it would hand an app back exactly the
// answer EdgeCORS withheld.
//
// SCOPED AND CONDITIONAL, both deliberately:
//
//   - only for origins the allowlist admits. A denied origin keeps its header and
//     is refused downstream exactly as before: this changes the allow path only.
//   - only for non-upgrade requests. A socket has no preflight and no ACAO, and
//     the origin check that guards cross-site WebSocket hijacking admits an EMPTY
//     Origin to let CLI clients in — clearing it there would fail OPEN.
//
// It is a parent middleware rather than a filter inside the child because the
// child is not ours to reach into: registered before the children are included,
// a parent's middleware is what they see first.
//
// IAM'S OWN ROUTES KEEP THE ORIGIN, and the verdict is held on the RESPONSE
// instead. IAM reads Origin for more than CORS: the site a wallet challenge names
// and verify re-derives (internal/wallet `site`), the sign-in link a mailed code
// carries (oidc `Landing`), and whether a request came from another page
// (sessions `Forgeable`, wallet `same`). Taken off, every one of them read "no
// page asked": a challenge asked from https://hanzo.ai named api.hanzo.ai, the
// host, and the wallet warned the person the sign-in was for another site. So on
// /v1/iam/* the child sees the request as the browser sent it, and whatever CORS
// headers it writes are replaced by the ones EdgeCORS wrote before it ran
// (`ruling`), so the edge's is still the only answer.
func EdgeOrigin() zip.Handler {
	return func(c *zip.Ctx) error {
		origin := c.Header("Origin")
		if origin == "" || c.Header("Upgrade") != "" || !CORSAllows(c.Context(), origin) {
			return c.Continue()
		}
		// The router matches the path as received, so a spelling that only
		// normalizes to an IAM route (an encoded or dotted path under another
		// route's wildcard) is not IAM's and keeps the strip.
		if raw := strings.ToLower(c.Path()); !(raw == "/v1/iam" || strings.HasPrefix(raw, "/v1/iam/")) || !addresses(c.Path(), iamRoutes) {
			c.Fiber().Request().Header.Del("Origin")
			return c.Continue()
		}
		h := &c.Fiber().Response().Header
		edge := rulingOf(h)
		// Deferred: a handler that panics has written its CORS headers already, and
		// the recovery that answers 500 sits outside this and resets nothing.
		defer edge.put(h)
		return c.Continue()
	}
}

// iamRoutes is the embedded IAM's address space: every route under it reads the
// request's Origin as IAM's own fact, and none of them answers CORS but the edge.
var iamRoutes = []string{"/v1/iam"}

// corsHeaders are the response headers a CORS answer is made of.
var corsHeaders = []string{
	"Access-Control-Allow-Origin",
	"Access-Control-Allow-Credentials",
	"Access-Control-Allow-Methods",
	"Access-Control-Allow-Headers",
	"Access-Control-Expose-Headers",
	"Access-Control-Max-Age",
}

// ruling is the CORS answer a response carried, by header; absent is no value.
type ruling map[string]string

// rulingOf reads the CORS answer h carries now.
func rulingOf(h *fasthttp.ResponseHeader) ruling {
	a := ruling{}
	for _, k := range corsHeaders {
		if v := h.Peek(k); len(v) > 0 {
			a[k] = string(v)
		}
	}
	return a
}

// put makes h carry exactly this answer: its headers set, every other CORS header
// removed, so nothing a child wrote survives.
func (a ruling) put(h *fasthttp.ResponseHeader) {
	for _, k := range corsHeaders {
		// Del first: Set replaces the first copy of a header and leaves the ones a
		// child appended.
		h.Del(k)
		if v, ok := a[k]; ok {
			h.Set(k, v)
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Per-IP edge rate limit
// ─────────────────────────────────────────────────────────────────────────────

// EdgeRateLimit returns the per-client-IP flood cap that runs BEFORE identity —
// the gateway's `qos/ratelimit/router` (strategy:"ip") role. The limit + window
// are read LIVE from the PLATFORM policy (per_ip_rpm / window_sec), so a SuperAdmin
// can retune the cap via PUT /v1/gateway/config with no redeploy. It counts on a
// Window over geo.IP's answer — an IPv6 caller by its /64 and by its /48 at
// sixteen times the limit — which evicts expired windows, so the count stays
// bounded at the edge's address cardinality.
//
// AN ADDRESS LIMIT IS FOR WHO HAS NOTHING ELSE TO BE LIMITED BY. It runs before
// identity, so it cannot tell a customer's key from a forgery, and it used to
// charge both to one per-IP count: a signed-in customer behind one address — a CI
// fleet, an office NAT, an agent fanning out — met the anonymous flood cap before
// its own key or org ceiling was ever asked. So the charge follows what the request
// turns out to be, in two halves:
//
//   - here, before identity: a request presenting NO credential is charged the
//     limit on every bucket, exactly as it always was. One presenting a credential
//     is charged only a BACKSTOP — edge.PresentedScale times the limit, on its /64 —
//     which bounds how much verification (a JWKS check, an IAM key lookup) one host
//     can make the boundary do, and caps no customer near their own ceiling;
//   - at the identity boundary (SanitizeIdentity asks nobody): a credential that
//     resolved to NOBODY — junk, expired, revoked, a publishable key — is charged
//     the limit on every bucket, /48 included, in the same count anonymous traffic
//     spends, and is a strike against the address. A flood of invalid credentials
//     meets the full per-IP limit; a header buys no way around it.
//
// The backstop is a bound on verification, so it is guarded three ways before CORS
// and identity run (see handler): a preflight counts as anonymous, an address that
// has spent its strikes presents nothing it would have to be asked about until its
// window resets, and an API key its own rate refused waits out that window here.
//
// A credential that resolves to a principal is charged no address count beyond the
// backstop: ScopeRateLimit holds it by its key and its org, which is what it is.
//
// SCOPE = public edge only. A request with no client address (geo.IP "") is an
// IN-CLUSTER direct caller — console BFF, a sibling hitting cloud.svc:8000 — the
// traffic the standalone gateway never saw, so it is not IP-limited here. A
// per_ip_rpm of 0 (env CLOUD_EDGE_RATELIMIT=false at boot) is a live no-op.
//
// look is the identity boundary's glance at a credential (identityValidator.glance):
// what it can say before identity runs. nil reads every credential as one that would
// ask IAM.
func EdgeRateLimit(pol *edge.Store, look func(*zip.Ctx) cloud.Glance) zip.Handler {
	rl := &edgeIPLimiter{policy: pol, look: look}
	return rl.handler
}

type edgeIPLimiter struct {
	policy *edge.Store
	look   func(*zip.Ctx) cloud.Glance
	// window is the per-IP count: every request presenting no credential, and every
	// credential the boundary resolved to nobody.
	window cloud.Window
	// presented is the backstop every request presenting a credential is charged.
	presented cloud.Window
	// strikes counts credentials the boundary resolved to nobody, per bucket of the
	// address that presented them; struck is each bucket that spent its limit of
	// them, barred until its window resets.
	strikes cloud.Window
	struck  cloud.Cache[string, barred]
	// refused is each API key its own rate refused, by credential, barred until that
	// window resets.
	refused cloud.Cache[string, barred]
}

// edgeDeferLocal carries the per-IP charge from the edge, which cannot yet tell a
// credential from a forgery, to the identity boundary, which can.
const edgeDeferLocal = "cloud.edgeDefer"

// edgeDeferral is that charge: the limiter, the caller's address, and the limit and
// window in force when the request arrived.
type edgeDeferral struct {
	rl     *edgeIPLimiter
	ip     string
	limit  int
	period time.Duration
}

func (rl *edgeIPLimiter) handler(c *zip.Ctx) error {
	p := rl.policy.Platform()
	limit := p.PerIPRPM
	if limit <= 0 {
		return c.Continue() // disabled at boot; no live cap.
	}
	period := time.Duration(p.WindowSec) * time.Second
	if period <= 0 {
		period = time.Second
	}
	ip := geo.IP(c)
	if ip == "" {
		// In-cluster/direct caller (no proxy hop): out of the edge's scope.
		return c.Continue()
	}
	// Counted by address.Buckets, never per address: one IPv6 host holds a whole /64
	// and a site a whole /48, and either can take a fresh address for every request,
	// so a bucket per /128 bounds nothing. geo.IP stays exact for every record that
	// keeps it.
	buckets := address.Buckets(ip)
	// A PREFLIGHT IS ANONYMOUS whatever it carries. EdgeCORS answers it before
	// identity is ever asked, so a credential on it — a key in the query string a
	// browser keeps on the preflight URL — is never checked, and would otherwise buy
	// the backstop for the one thing a preflight can make this process do: resolve a
	// fresh Origin, a plane hop per caller-chosen hostname.
	if anonymous(c) || preflight(c) {
		left, reset := rl.window.Spend(buckets, limit, period)
		if err := pace(c, limit, left, reset); err != nil {
			return err
		}
		return c.Continue()
	}
	// AN ADDRESS THAT HAS SPENT ITS STRIKES IS TOLD NOTHING IT HAS TO ASK FOR until
	// its window resets. Every credential it presented past its limit was nobody, and
	// finding that out cost a lookup — IAM for a key, the plane for a fresh Origin in
	// CORS — before the boundary could say so; a customer whose credentials resolve
	// never strikes. So while an address is struck, only a credential the boundary
	// can tell is somebody WITHOUT asking passes (identityValidator.glance): a JWT
	// verified against the cached JWKS, a key whose cached answer is a principal.
	// Everything else — an uncached key, a forged token, a key the boundary never
	// reads — is refused here, ahead of CORS and identity, so the cost of asking
	// stops at the limit, and a struck /48 (sixteen limits of junk from its /64s, not
	// 65,536) does not turn away its signed-in neighbours.
	if h, ok := rl.struckFor(buckets); ok && !rl.glanceAt(c).Somebody {
		return pace(c, h.limit, -1, h.until)
	}
	// AN API KEY ITS OWN RATE REFUSED IS REFUSED HERE until that window resets, so a
	// key past its own limit costs neither an IAM lookup (a member's key is asked on
	// every request) nor a count. Keyed by the credential alone: a key's own ceiling
	// is the same whatever org a request names. Not for a transcript push the key
	// opened (continues), which no ceiling counts; and not once the boundary can see
	// the key's rate has been raised past the limit it was refused at — a key whose
	// answer is not cached (a member's) cannot be seen, and waits out the window.
	cred := credentialOf(c)
	if cred != "" && !continues(c) {
		if h, ok := rl.refused.Get(cred); ok {
			if g := rl.glanceAt(c); !g.Somebody || (g.Rate > 0 && g.Rate <= h.limit) {
				return pace(c, h.limit, -1, h.until)
			}
		}
	}
	// THE BACKSTOP HOLDS ONLY THE /64. Every /64 in a /48 shares the wider bucket,
	// so a neighbour could fill it, and whether this credential is anybody is about
	// to be known: a /48 of junk is struck at the boundary, never charged to the
	// customers beside it. It speaks only when it refuses — it is not the ceiling a
	// caller whose credential validates paces by.
	backstop := limit * edge.PresentedScale
	if left, reset := rl.presented.Spend(buckets[:1], backstop, period); left < 0 {
		return pace(c, backstop, left, reset)
	}
	c.Fiber().Locals(edgeDeferLocal, &edgeDeferral{rl: rl, ip: ip, limit: limit, period: period})
	err := c.Continue()
	if h, ok := c.Fiber().Locals(rateBarredLocal).(barred); ok && cred != "" {
		rl.refused.PutUntil(cred, h, h.until)
	}
	return err
}

// nobody answers a request that presented a credential the identity boundary
// resolved to no principal. It is charged the per-IP limit on every bucket of its
// address, in the count anonymous traffic spends, and refused 429 past it;
// otherwise it gets the refusal the boundary was already giving it (nil to
// continue). It is also a strike against the address (strike) — unless IAM gave no
// answer, which is nothing the address did. SanitizeIdentity
// asks it at every exit that resolves nobody. A request the edge deferred nothing
// for — anonymous, in-cluster, or a boundary with no edge in front of it — has
// nothing to charge here.
func nobody(c *zip.Ctx, refusal error, strike bool) error {
	d, ok := c.Fiber().Locals(edgeDeferLocal).(*edgeDeferral)
	if !ok {
		return refusal
	}
	if strike {
		d.rl.strike(d.ip, d.limit, d.period)
	}
	left, reset := d.rl.window.Spend(address.Buckets(d.ip), d.limit, d.period)
	if err := pace(c, d.limit, left, reset); err != nil {
		return err
	}
	return refusal
}

// glanceAt is the boundary's glance at c's credential, or nothing known.
func (rl *edgeIPLimiter) glanceAt(c *zip.Ctx) cloud.Glance {
	if rl.look == nil {
		return cloud.Glance{}
	}
	return rl.look(c)
}

// struckFor is the bar on the first of buckets that is struck, if any.
func (rl *edgeIPLimiter) struckFor(buckets []address.Bucket) (barred, bool) {
	for _, b := range buckets {
		if h, ok := rl.struck.Get(b.Key); ok {
			return h, true
		}
	}
	return barred{}, false
}

// strike counts one credential that resolved to nobody against each bucket of the
// address that presented it, and bars a bucket that has spent its limit of them —
// the per-IP limit, sixteen times it for a /48 — until that bucket's window resets.
func (rl *edgeIPLimiter) strike(ip string, limit int, period time.Duration) {
	for _, b := range address.Buckets(ip) {
		if left, reset := rl.strikes.Spend([]address.Bucket{b}, limit, period); left <= 0 {
			rl.struck.PutUntil(b.Key, barred{until: reset, limit: limit * b.Scale}, reset)
		}
	}
}

// preflight reports whether a request is a CORS preflight: an OPTIONS carrying the
// Access-Control-Request-Method that makes it one (Fetch, CORS preflight request).
// A bare OPTIONS is a different question (RFC 9110 §9.3.7) and is not one.
func preflight(c *zip.Ctx) bool {
	return c.Method() == "OPTIONS" && c.Header("Access-Control-Request-Method") != ""
}

// anonymous reports whether a request presents no credential the identity boundary
// could make a principal from — the token callerToken reads (a bearer, Basic, a
// session cookie, an sk- key on X-Api-Key), other than a publishable key. Presence,
// not validity: the edge runs before identity, which decides what it is worth.
//
// A pk- key, and a key on Api-Key or ?api_key=, are the business of the handler
// that reads them (event ingest attributes a beacon by one). Identity never makes a
// principal of them, so they buy no backstop here and are counted as the anonymous
// traffic they are to this boundary — and nobody holds them against the address.
func anonymous(c *zip.Ctx) bool {
	tok := cloud.CallerToken(c)
	return tok == "" || cloud.IsPublishableKey(tok)
}
