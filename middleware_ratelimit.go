package cloud

// ScopeRateLimit — the ONE per-scope request-rate limiter (issue #70). It caps
// requests/min per (org, project, service) scope using the rate limit an org
// configures on its spend-alert rows (RateLimitRpm). It is DISTINCT from the
// per-IP pre-auth limiters (clients/kms login, clients/crm intake): those
// throttle anonymous abuse by IP BEFORE identity; this throttles an authenticated
// org's own configured ceiling per scope, keyed off the VALIDATED principal.
//
// Composition, not duplication: the token-bucket mechanics are the proven
// zip/middleware.RateLimit primitive. This middleware only resolves the DYNAMIC
// per-scope limit and routes the request to the bucket for that limit — one
// zip.RateLimit instance per distinct rpm, its buckets keyed by the scope. The
// bucket key is stashed in a request-local so the shared instance's KeyFn returns
// the scope this request resolved to.
//
// Most-restrictive-wins: among the covering rules (org-wide, project, service),
// the smallest rpm binds, and the request is bucketed at that rule's scope — so a
// tighter project/service limit never leaks across projects and an org-wide limit
// applies to every request that has no tighter rule.
//
// Fail-open: the limit config is fetched from commerce and cached with a short
// TTL; if commerce is unreachable the request is NOT limited (the funds/spend-cap
// gate still applies). A rate-limit outage must never take down paid traffic.

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/clients/gateway/edge"
	"github.com/hanzoai/cloud/clients/metering"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
	zipmw "github.com/zap-proto/zip/middleware"
)

// rateScopeKeyLocal is the request-local key carrying the resolved scope bucket
// key from the middleware to the shared zip.RateLimit instance's KeyFn.
const rateScopeKeyLocal = "cloud.rateScopeKey"

// rateConfigTTL bounds how stale a cached per-org rate-limit config may be. Short
// so an org's limit change takes effect within seconds, long enough that the
// config fetch is amortized far below the request rate.
const rateConfigTTL = 5 * time.Second

// ScopeRateLimit returns the per-scope rate-limit middleware. It caps an
// authenticated org from TWO config sources, most-restrictive-wins:
//   - commerce spend-alert RateLimitRpm (the plan-configured ceiling), and
//   - the /v1/gateway per-org OrgRPM (gp), the runtime-mutable operator override.
//
// It is a no-op passthrough only when BOTH are absent (no commerce AND no policy
// store), so an unwired deployment is never blocked — mirroring BillingGate.
func ScopeRateLimit(m *metering.Client, gp *edge.Store) zip.Handler {
	if !billingEnabled(m) && gp == nil {
		return func(c *zip.Ctx) error { return c.Next() }
	}
	rl := &scopeRateLimiter{
		m:       m,
		gp:      gp,
		ttl:     rateConfigTTL,
		cache:   map[string]scopeCacheEntry{},
		buckets: map[int]zip.Handler{},
	}
	return rl.handler
}

type scopeCacheEntry struct {
	rules  []metering.ScopeRule
	expiry time.Time
}

type scopeRateLimiter struct {
	m   *metering.Client
	gp  *edge.Store // /v1/gateway per-org OrgRPM override (nil-safe).
	ttl time.Duration

	mu      sync.Mutex
	cache   map[string]scopeCacheEntry
	buckets map[int]zip.Handler // one zip.RateLimit per distinct rpm; buckets keyed by scope.
}

func (rl *scopeRateLimiter) handler(c *zip.Ctx) error {
	// Never gate the co-resident commerce surface — it is this limiter's OWN
	// config source, not metered user traffic. rulesFor reads the scope rules via
	// GET /v1/billing/spend-alerts, dispatched in-process over the co-resident
	// commerce handler (commerceinproc), which re-runs the WHOLE app. If this
	// handler gated that path, the rules fetch would re-enter here, re-fetch (the
	// cache is only filled AFTER the fetch returns, so it is still cold), and
	// re-enter again — an unbounded in-process self-dispatch that overflowed the
	// writer's goroutine stack and piled up setRequestCancel goroutines. The
	// billing/commerce surfaces are internal S2S plumbing; the per-IP pre-auth
	// limiters and commerce's own gates still apply, so exempting them here removes
	// the self-reference without loosening any user-facing ceiling.
	if p := c.Path(); strings.HasPrefix(p, "/v1/billing/") ||
		strings.HasPrefix(p, "/v1/commerce/") ||
		strings.HasPrefix(p, "/_/commerce/") {
		return c.Next()
	}

	// Only an authenticated org is scope-rate-limited. Without a validated
	// principal there is no org to key on; anonymous abuse is handled by the
	// per-IP pre-auth limiters, and priced paths are refused by each subsystem's
	// own principal gate.
	org, ok := principal.Org(c)
	if !ok {
		return c.Next()
	}
	project := principal.Project(c)
	service := canonicalService(c.Path())

	key, rpm := bindingRateRule(rl.rulesFor(c.Context(), org), org, project, service)

	// The /v1/gateway per-org OrgRPM is the runtime operator override. It binds
	// when set and tighter than (or in the absence of) any commerce rule —
	// most-restrictive-wins, in an org-scoped bucket namespace distinct from the
	// commerce scope keys so the two never share a bucket. gp is nil-safe.
	if orpm := rl.gp.OrgRPM(org); orpm > 0 && (rpm <= 0 || orpm < rpm) {
		key, rpm = "gwpolicy|"+org, orpm
	}
	if rpm <= 0 {
		return c.Next() // no rate limit configured for this scope.
	}

	// Route to the bucket for this rpm; the shared instance's KeyFn reads the
	// scope key we resolved, so buckets are isolated per scope.
	c.Fiber().Locals(rateScopeKeyLocal, key)
	return rl.bucketFor(rpm)(c)
}

// bindingRateRule picks the MOST RESTRICTIVE covering rate rule and returns the
// bucket key at that rule's scope plus its rpm. Returns ("",0) when no covering
// rule sets a rate limit. Covering: each of a rule's axes is the wildcard "" or
// equals the request's (with the default project folded onto "").
func bindingRateRule(rules []metering.ScopeRule, org, project, service string) (string, int) {
	if principal.IsDefaultProject(project) {
		project = ""
	}
	best := 0
	bestKey := ""
	for _, r := range rules {
		if r.RateLimitRpm <= 0 {
			continue
		}
		covers := (r.Project == "" || r.Project == project) &&
			(r.Service == "" || r.Service == service)
		if !covers {
			continue
		}
		if best == 0 || r.RateLimitRpm < best {
			best = r.RateLimitRpm
			bestKey = scopeBucketKey(org, r.Project, r.Service)
		}
	}
	return bestKey, best
}

// scopeBucketKey is the rate-limit bucket identity for a scope. The org prefix is
// the hard org boundary — a bucket can never be shared across orgs, so org A's
// limit can never throttle org B.
func scopeBucketKey(org, project, service string) string {
	return org + "|" + project + "|" + service
}

// bucketFor returns the shared zip.RateLimit instance for an rpm, creating it
// once. All scopes with the same rpm share the instance but get SEPARATE buckets
// via the scope-keyed KeyFn — so the token-bucket mechanics are reused, never
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

// rulesFor returns the org's rate-limit rules, cached with a short TTL. On a
// commerce fetch error it fails OPEN (empty rules) and caches that briefly so a
// commerce blip neither blocks traffic nor hammers commerce. The fetch runs on a
// bounded background context so a client disconnect can't poison the cache.
func (rl *scopeRateLimiter) rulesFor(reqCtx context.Context, org string) []metering.ScopeRule {
	if !billingEnabled(rl.m) {
		return nil // no commerce configured — only the /v1/gateway OrgRPM applies.
	}
	rl.mu.Lock()
	e, ok := rl.cache[org]
	rl.mu.Unlock()
	if ok && time.Now().Before(e.expiry) {
		return e.rules
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	rules, err := rl.m.ScopeRules(ctx, org)
	if err != nil {
		rules = nil // fail open.
	}

	rl.mu.Lock()
	rl.cache[org] = scopeCacheEntry{rules: rules, expiry: time.Now().Add(rl.ttl)}
	rl.mu.Unlock()
	return rules
}
