package cloud

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
// otherwise trigger. That is exactly, and only, what the gateway's IP-strategy
// router rate limit did.

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/apps/gateway/edge"
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
// An origin is admitted from either of two sources (cors_origin.go):
//
//	DECLARED — the PLATFORM policy's CORSOrigins, read live (recompiled only when it
//	           changes) so a SuperAdmin can retune it via PUT /v1/gateway/config.
//	PROVEN   — a host whose site_hosts row is VERIFIED and bound to an org, i.e. the
//	           customer published the DNS-01 challenge token for it.
//
// PROVEN is why this is not a static list any more. The product is that a customer
// forks hanzoai/console and deploys it on their OWN domain; a list of domains we own
// can never name that domain, so the shipped feature could not work. It does not
// widen trust: the name still has to be proved, and it is proved through the
// existing boundary rather than a second one.
//
// AN EMPTY DECLARED LIST IS NO LONGER A NO-OP. It used to be, on the reasoning that
// the shared Traefik ingress owned CORS and a second Access-Control-Allow-Origin
// would break every preflight. That reasoning still holds for the header, and this
// middleware still emits one only when it admits the origin — but PROVEN has to be
// answerable even in a deployment that declared nothing, or a customer's own domain
// would depend on an operator having typed something into a list.
//
// The preflight is short-circuited (204) and the actual response reflects the
// origin. Both hang off the SAME boolean, so there is no arrangement in which a
// browser is told yes at the preflight and no at the request.
func EdgeCORS(pol *edge.Store) zip.Handler {
	return edgeCORS(pol, corsVerifiedHost)
}

// edgeCORS is EdgeCORS over an explicit PROVEN source — the seam the tests drive
// without standing up a projects store or a plane.
func edgeCORS(pol *edge.Store, proven verifiedHostFn) zip.Handler {
	var (
		mu      sync.Mutex
		lastKey string
		origins = &corsOrigins{proven: proven}
	)
	// Publish the instance, not a copy: apps/ai asks THIS predicate, sharing its
	// compiled allowlist and its cache, so the two layers cannot drift.
	corsPredicate.Store(origins)
	// current recompiles the DECLARED matcher only when the live allowlist string
	// changes; the list is small and Platform() is itself TTL-cached, so this stays
	// cheap. The PROVEN cache is deliberately NOT rebuilt with it — it is keyed on
	// hostnames, not on the policy, and it ages out on its own TTL.
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

		if !current().allowed(c.Context(), origin) {
			// An origin we do not vouch for gets NO credentialed CORS headers, and
			// the browser blocks the read. It is not refused here: this middleware
			// is in front of every route, and a 403 would break the many non-browser
			// callers that send a stray Origin, plus every same-origin POST whose
			// host nobody thought to declare. Withholding the header is the whole
			// enforcement — it is what the browser acts on.
			return c.Continue()
		}
		// Admitted: reflect the exact origin. Credentialed CORS is NEVER wildcard —
		// `*` with Access-Control-Allow-Credentials is invalid per the Fetch
		// standard, and the value echoed here has already been matched against a
		// declaration or resolved to a verified record.
		c.SetHeader("Access-Control-Allow-Origin", origin)
		c.SetHeader("Access-Control-Allow-Credentials", "true")
		if c.Method() == "OPTIONS" {
			// The allow-headers answer now depends on what the browser asked, so the
			// cache key has to say so for the same reason Vary: Origin does.
			c.Fiber().Vary("Access-Control-Request-Headers")
			c.SetHeader("Access-Control-Allow-Methods", corsAllowMethods)
			c.SetHeader("Access-Control-Allow-Headers", corsAllowHeaders(c.Header("Access-Control-Request-Headers")))
			c.SetHeader("Access-Control-Max-Age", corsMaxAge)
			// Short-circuit the preflight: 204, no body, no auth/rate work.
			c.Status(204)
			return nil
		}
		return c.Continue()
	}
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

// CORSAllows reports whether this deployment's CORS authority admits origin.
//
// Exported for apps/ai, which is the only caller and uses it to keep the mounted ai
// module's own filter inert. Answers false before the edge is built (no app, no
// policy, nothing admitted), which is the fail-secure direction.
func CORSAllows(ctx context.Context, origin string) bool {
	o := corsPredicate.Load()
	if o == nil {
		return false
	}
	return o.allowed(ctx, origin)
}

// ─────────────────────────────────────────────────────────────────────────────
// Per-IP edge rate limit
// ─────────────────────────────────────────────────────────────────────────────

// EdgeRateLimit returns the per-client-IP flood cap that runs BEFORE identity —
// the gateway's `qos/ratelimit/router` (strategy:"ip") role. The limit + window
// are read LIVE from the PLATFORM policy (per_ip_rpm / window_sec), so a SuperAdmin
// can retune the cap via PUT /v1/gateway/config with no redeploy. It is a
// fixed-window counter keyed on the client IP (leftmost X-Forwarded-For, via
// ClientIP) with opportunistic eviction of expired windows so the map stays bounded
// even at the edge's high IP cardinality (why this is not the zip token-bucket
// primitive that ScopeRateLimit reuses: that primitive never evicts, which is fine
// for a bounded per-org keyspace but would grow without bound keyed on raw IPs).
//
// SCOPE = public edge only. A request with NO X-Forwarded-For is an IN-CLUSTER
// direct caller (console BFF, sibling service hitting cloud.svc:8000) — it never
// transited the ingress/LB, exactly the traffic the standalone gateway never saw,
// so it is not IP-limited here (parity: the gateway only ever rate-limited public
// traffic). Only proxied edge traffic, which carries the real client IP in XFF,
// is throttled. A per_ip_rpm of 0 (env CLOUD_EDGE_RATELIMIT=false at boot) is a
// live no-op.
func EdgeRateLimit(pol *edge.Store) zip.Handler {
	rl := &edgeIPLimiter{policy: pol, buckets: map[string]*edgeBucket{}}
	return rl.handler
}

type edgeBucket struct {
	count int
	reset time.Time
}

type edgeIPLimiter struct {
	policy *edge.Store

	mu        sync.Mutex
	buckets   map[string]*edgeBucket
	lastSweep time.Time
}

func (rl *edgeIPLimiter) handler(c *zip.Ctx) error {
	p := rl.policy.Platform()
	limit := p.PerIPRPM
	if limit <= 0 {
		return c.Continue() // disabled at boot; no live cap.
	}
	window := time.Duration(p.WindowSec) * time.Second
	if window <= 0 {
		window = time.Second
	}
	ip := ClientIP(c)
	if ip == "" {
		// In-cluster/direct caller (no proxy hop): out of the edge's scope.
		return c.Continue()
	}
	now := time.Now()

	rl.mu.Lock()
	rl.sweepLocked(now, window)
	b, ok := rl.buckets[ip]
	if !ok || now.After(b.reset) {
		b = &edgeBucket{reset: now.Add(window)}
		rl.buckets[ip] = b
	}
	b.count++
	count := b.count
	rl.mu.Unlock()

	if count > limit {
		c.SetHeader("X-RateLimit-Limit", itoaEdge(limit))
		c.SetHeader("X-RateLimit-Remaining", "0")
		return zip.Errorf(429, "rate limit exceeded")
	}
	c.SetHeader("X-RateLimit-Limit", itoaEdge(limit))
	c.SetHeader("X-RateLimit-Remaining", itoaEdge(limit-count))
	return c.Continue()
}

// sweepLocked evicts expired windows at most once per window, amortizing the O(n)
// scan far below the request rate so the bucket map never accumulates one entry
// per IP ever seen. Caller holds rl.mu.
func (rl *edgeIPLimiter) sweepLocked(now time.Time, window time.Duration) {
	if now.Sub(rl.lastSweep) < window {
		return
	}
	rl.lastSweep = now
	for ip, b := range rl.buckets {
		if now.After(b.reset) {
			delete(rl.buckets, ip)
		}
	}
}

// itoaEdge is a tiny allocation-free int→string for the X-RateLimit headers,
// matching the zip primitive's own helper (kept local so this file has no
// dependency on middleware internals).
func itoaEdge(n int) string {
	if n <= 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for n > 0 {
		pos--
		b[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(b[pos:])
}
