package console

// Per-IP rate limiting for the abuse-sensitive console write routes (hk- key
// mint/rotate/revoke, HUSD wallet top-up). This is DISTINCT from commerce's spend-cap
// (ScopeRateLimit): it caps request FREQUENCY per client IP to blunt brute-force /
// enumeration / resource-exhaustion, restoring the edge protection cloud loses when a
// caller reaches it DIRECTLY (bypassing the gateway/ingress limiter) on the money path.
//
// A token bucket per IP: `perMin` tokens refilled continuously, burst == perMin. In
// memory (cloud is single-replica for this surface); multi-replica would be a per-
// replica soft limit — acceptable defense-in-depth, not a hard global quota. Idle
// buckets are evicted lazily so the map cannot grow without bound.

import (
	"net"
	"strings"
	"sync"
	"time"

	"github.com/zap-proto/zip"
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

type rateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tokenBucket
	perSec  float64
	burst   float64
	ttl     time.Duration // evict buckets idle longer than this
	lastGC  time.Time
}

// newRateLimiter builds a limiter allowing `perMin` requests/minute per IP (burst ==
// perMin). perMin<=0 disables limiting (allow always).
func newRateLimiter(perMin int) *rateLimiter {
	return &rateLimiter{
		buckets: map[string]*tokenBucket{},
		perSec:  float64(perMin) / 60.0,
		burst:   float64(perMin),
		ttl:     10 * time.Minute,
		lastGC:  time.Now(),
	}
}

// allow reports whether a request from key (IP) may proceed, consuming one token.
func (r *rateLimiter) allow(key string) bool {
	if r.perSec <= 0 {
		return true
	}
	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()

	if now.Sub(r.lastGC) > r.ttl {
		for k, b := range r.buckets {
			if now.Sub(b.last) > r.ttl {
				delete(r.buckets, k)
			}
		}
		r.lastGC = now
	}

	b := r.buckets[key]
	if b == nil {
		b = &tokenBucket{tokens: r.burst, last: now}
		r.buckets[key] = b
	}
	// Refill.
	b.tokens += now.Sub(b.last).Seconds() * r.perSec
	if b.tokens > r.burst {
		b.tokens = r.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// clientIP resolves the caller's IP for rate keying: the FIRST hop of
// X-Forwarded-For (the original client, as set by hanzoai/ingress), else the socket
// peer. A forgeable XFF only lets an attacker SPREAD their own budget across keys, not
// consume another client's — the limiter is a coarse abuse cap, not an authz control.
func clientIP(c *zip.Ctx) string {
	if xff := strings.TrimSpace(c.Header("X-Forwarded-For")); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			xff = xff[:i]
		}
		if ip := strings.TrimSpace(xff); ip != "" {
			return ip
		}
	}
	ip := c.Fiber().IP()
	if host, _, err := net.SplitHostPort(ip); err == nil {
		return host
	}
	return ip
}

// rateLimit wraps a handler, refusing 429 when the caller's IP exceeds rl.
func (s *svc) rateLimit(rl *rateLimiter, next zip.Handler) zip.Handler {
	return func(c *zip.Ctx) error {
		if !rl.allow(clientIP(c)) {
			return zip.Errorf(429, "rate limit exceeded; retry shortly")
		}
		return next(c)
	}
}
