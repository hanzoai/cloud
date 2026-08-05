// SPDX-License-Identifier: Apache-2.0

package cloud

// ScopeRateLimit — the per-org request-rate limiter (issue #70). It caps
// requests/min per org using the /v1/gateway per-org OrgRPM, the runtime-mutable
// operator override served by the gateway config plane (edge.Store). It is
// DISTINCT from the per-IP pre-auth limiters (EdgeRateLimit): those throttle
// anonymous abuse by IP BEFORE identity; this throttles an authenticated org's
// own configured ceiling, keyed off the VALIDATED principal.
//
// Composition, not duplication: the token-bucket mechanics are the proven
// zip/middleware.RateLimit primitive. This middleware only resolves the DYNAMIC
// per-org limit and routes the request to the bucket for that limit — one
// zip.RateLimit instance per distinct rpm, its buckets keyed by the org.
//
// The OSS core caps on the operator-set OrgRPM alone. The private build layers a
// plan-configured commerce ceiling on top (most-restrictive-wins); that path is
// not part of the OSS binary.

import (
	"sync"
	"time"

	"github.com/hanzoai/cloud/clients/gateway/edge"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
	zipmw "github.com/zap-proto/zip/middleware"
)

// rateScopeKeyLocal is the request-local key carrying the resolved bucket key
// from the middleware to the shared zip.RateLimit instance's KeyFn.
const rateScopeKeyLocal = "cloud.rateScopeKey"

// ScopeRateLimit returns the per-org rate-limit middleware. It caps an
// authenticated org at the /v1/gateway per-org OrgRPM (gp). It is a no-op
// passthrough when no policy store is present, so an unwired deployment is never
// blocked.
func ScopeRateLimit(gp *edge.Store) zip.Handler {
	if gp == nil {
		return func(c *zip.Ctx) error { return c.Next() }
	}
	rl := &scopeRateLimiter{
		gp:      gp,
		buckets: map[int]zip.Handler{},
	}
	return rl.handler
}

type scopeRateLimiter struct {
	gp *edge.Store // /v1/gateway per-org OrgRPM override (nil-safe).

	mu      sync.Mutex
	buckets map[int]zip.Handler // one zip.RateLimit per distinct rpm; buckets keyed by org.
}

func (rl *scopeRateLimiter) handler(c *zip.Ctx) error {
	// Only an authenticated org is scope-rate-limited. Without a validated
	// principal there is no org to key on; anonymous abuse is handled by the
	// per-IP pre-auth limiters.
	org, ok := principal.Org(c)
	if !ok {
		return c.Next()
	}

	rpm := rl.gp.OrgRPM(org)
	if rpm <= 0 {
		return c.Next() // no per-org ceiling configured.
	}

	// Route to the bucket for this rpm; the shared instance's KeyFn reads the org
	// key we resolved, so buckets are isolated per org.
	c.Fiber().Locals(rateScopeKeyLocal, "gwpolicy|"+org)
	return rl.bucketFor(rpm)(c)
}

// bucketFor returns the shared zip.RateLimit instance for an rpm, creating it
// once. All orgs with the same rpm share the instance but get SEPARATE buckets
// via the org-keyed KeyFn — so the token-bucket mechanics are reused, never
// re-implemented.
func (rl *scopeRateLimiter) bucketFor(rpm int) zip.Handler {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	if h, ok := rl.buckets[rpm]; ok {
		return h
	}
	h := zipmw.RateLimit(zipmw.RateLimitConfig{
		Limit:  rpm,
		Window: time.Minute,
		KeyFn: func(c *zip.Ctx) string {
			if v, ok := c.Fiber().Locals(rateScopeKeyLocal).(string); ok && v != "" {
				return v
			}
			return c.Org() // defensive fallback; the middleware always sets the local.
		},
	})
	rl.buckets[rpm] = h
	return h
}
