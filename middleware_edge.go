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
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/clients/gateway/edge"
	"github.com/zap-proto/zip"
)

// ─────────────────────────────────────────────────────────────────────────────
// CORS
// ─────────────────────────────────────────────────────────────────────────────

// corsAllowMethods / corsAllowHeaders / corsMaxAge mirror the gateway's ONE CORS
// policy (hanzoai/gateway routes.go corsPreflightMiddleware) so the browser
// contract is byte-identical whether cloud is reached through the gateway or
// directly. Credentialed reflect-Origin: the allowlisted request Origin is echoed
// verbatim (never `*` with credentials), so a cookie/Authorization request from an
// allowed brand host works and every other origin gets no CORS headers.
const (
	corsAllowMethods = "GET, POST, PUT, PATCH, DELETE, OPTIONS"
	corsAllowHeaders = "Content-Type, Authorization, X-User-Id, X-Org-Id, " +
		"X-Project-Id, X-Environment, X-Roles, X-User-Email, X-Request-ID, " +
		"X-Client-ID, X-Requested-With, Accept, Accept-Language"
	corsMaxAge = "86400"
)

// EdgeCORS returns the browser-CORS middleware for the public /v1 edge. The origin
// allowlist is the PLATFORM policy's CORSOrigins, read live (recompiled only when
// it changes) so a SuperAdmin can add/remove origins via PUT /v1/gateway/config.
//
// DEFAULT OFF (empty allowlist ⇒ no-op passthrough). On the RECOMMENDED rollout —
// the shared Traefik ingress keeps fronting api.hanzo.ai and its `cors-allow-all`
// middleware already answers CORS there — enabling cloud CORS too would emit a
// SECOND Access-Control-Allow-Origin header and break every browser preflight. So
// CORS stays owned by exactly ONE layer: the ingress until/unless the edge moves to
// a direct DO-LB→cloud path (cloud terminates TLS for api.hanzo.ai), at which point
// the operator sets CLOUD_CORS_ORIGINS (or PUTs it) and cloud becomes the sole CORS
// authority. One policy, one place — never both.
//
// When enabled it handles the OPTIONS preflight itself (204, short-circuit) and
// reflects the allowlisted Origin on the actual response, then continues the chain.
func EdgeCORS(pol *edge.Store) zip.Handler {
	var (
		mu      sync.Mutex
		lastKey string
		matcher *originMatcher
	)
	// currentMatcher recompiles only when the live allowlist string changes; the
	// list is small and Platform() is itself TTL-cached, so this stays cheap.
	currentMatcher := func() *originMatcher {
		origins := pol.Platform().CORSOrigins
		key := strings.Join(origins, "\n")
		mu.Lock()
		defer mu.Unlock()
		if key != lastKey {
			lastKey = key
			matcher = newOriginMatcher(origins)
		}
		return matcher
	}
	return func(c *zip.Ctx) error {
		m := currentMatcher()
		if m == nil {
			// No allowlist configured ⇒ CORS is owned elsewhere (ingress). No-op.
			return c.Continue()
		}
		origin := c.Header("Origin")
		if origin == "" || !m.allowed(origin) {
			// Not a cross-origin browser request we vouch for: add nothing (a
			// non-allowlisted origin never receives credentialed CORS headers).
			// A preflight from an unknown origin falls through and is refused by
			// the normal pipeline; the browser blocks it either way.
			return c.Continue()
		}
		// Allowlisted origin: reflect it (credentialed CORS is never wildcard).
		c.SetHeader("Access-Control-Allow-Origin", origin)
		c.SetHeader("Access-Control-Allow-Credentials", "true")
		c.SetHeader("Vary", "Origin")
		if c.Method() == "OPTIONS" {
			c.SetHeader("Access-Control-Allow-Methods", corsAllowMethods)
			c.SetHeader("Access-Control-Allow-Headers", corsAllowHeaders)
			c.SetHeader("Access-Control-Max-Age", corsMaxAge)
			// Short-circuit the preflight: 204, no body, no auth/rate work.
			c.Status(204)
			return nil
		}
		return c.Continue()
	}
}

// originMatcher decides whether a request Origin is on the CORS allowlist. Each
// config entry is either an EXACT origin ("https://hanzo.ai") or a host wildcard
// ("*.hanzo.ai", which matches the apex `hanzo.ai` AND any subdomain
// `<sub>.hanzo.ai`) — the two forms the gateway/ingress allowlists use, expressed
// once. Bare-host entries ("hanzo.ai") are treated as a host match on any scheme.
type originMatcher struct {
	exact  map[string]struct{} // full origin strings, e.g. "https://hanzo.ai"
	hosts  map[string]struct{} // bare hosts matched regardless of scheme
	suffix []string            // wildcard hosts: apex value, e.g. "hanzo.ai"
}

// newOriginMatcher compiles the allowlist. Returns nil when empty, so the caller
// can make CORS a pure no-op (the "owned elsewhere" default).
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
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
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
