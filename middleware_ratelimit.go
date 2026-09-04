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
// Fail-open: the limit config is asked of commerce over the internal plane and
// cached with a short TTL; if commerce is unreachable the request is NOT limited
// (the funds/spend-cap gate still applies). A rate-limit outage must never take
// down paid traffic.

import (
	"context"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/client/commerce"
	"github.com/hanzoai/cloud/metering"
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

// scopeRulesTimeout bounds the plane read. A rate ceiling is a POLICY overlay,
// never a gate on availability, so a slow commerce must fail open FAST rather
// than hold the request that asked.
const scopeRulesTimeout = 3 * time.Second

// defaultServiceRPM is the ceiling a SERVICE CLASS carries when nothing has
// configured one for it. It is the floor under the two config sources, not an
// override of them: a configured rule still wins by most-restrictive-wins, and
// an org that has never been configured is still bounded.
//
// It exists because "no rule" used to mean "no limit". For a priced service that
// is defensible — consumption bills, so an unbounded caller is an unbounded
// invoice, and the funds gate ends it. It is not defensible for a service that
// bills ZERO: the speech models are unpriced by design, so nothing downstream
// ever says stop, and they are served from a two-replica CPU deployment that is
// the whole estate's capacity for them. Free and unbounded is a free denial of
// service, and the endpoint that most needs a limit was the one relying on an
// operator to remember to set one.
//
// Keyed by the class canonicalService derives, so it is deliberately narrow: a
// class not named here behaves exactly as before. A platform-wide default is a
// separate decision with a blast radius this is not the change to take.
var defaultServiceRPM = map[string]int{
	// /v1/audio/* — transcription and synthesis. One request per second sustained
	// is far above interactive use and far below what one org could use to
	// monopolize the upstream, which the in-flight ceiling in hanzoai/ai bounds
	// separately. The two compose: this bounds how OFTEN one org may ask, that
	// bounds how MUCH work can be running at once.
	"audio": 60,
	// /v1/sbom/* — the bill of materials of an image. Unpriced by design, and a
	// LOOKUP THAT MISSES is not a lookup: it opens a connection to the registry,
	// reads the attached document and writes the shared table, all on the asking.
	// A console renders one image at a time, so one per second sustained is far
	// above what the surface is for and is the only thing that says stop.
	"sbom": 60,
}

// ScopeRateLimit returns the per-scope rate-limit middleware. It caps an
// authenticated org from TWO config sources plus a static floor,
// most-restrictive-wins:
//   - commerce spend-alert RateLimitRpm (the plan-configured ceiling),
//   - the /v1/gateway per-org OrgRPM (gp), the runtime-mutable operator override,
//   - defaultServiceRPM, the ceiling a service class carries unconfigured.
//
// It is a no-op passthrough only when all three are absent, so an unwired
// deployment is never blocked — mirroring BillingGate — while a service class
// that names a default is bounded even in one.
func ScopeRateLimit(m *metering.Client, gp *edge.Store) zip.Handler {
	if !billingEnabled(m) && gp == nil && len(defaultServiceRPM) == 0 {
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
	// Never gate the commerce billing surface: it is internal S2S plumbing, not
	// metered user traffic, and the per-IP pre-auth limiters plus commerce's own
	// gates still cover it — so exempting it loosens no user-facing ceiling.
	// (rulesFor no longer reaches commerce through this app, so this is policy
	// now and not a self-reference guard; see rulesFor.)
	// Compared against the ROUTER's path, not the raw spelling: a prefix test over
	// c.Path() answers a question about how the client typed the URL, while the
	// exemption is about which handler will run (see cloud.RoutePath). ONE
	// normalization, the same one the abuse gate and the grant list use.
	path := RoutePath(c.Path())
	for _, p := range []string{"/v1/billing/", "/v1/commerce/", "/_/commerce/"} {
		if underPrefix(path, p) {
			return c.Next()
		}
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
	service := canonicalService(path)

	key, rpm := bindingRateRule(rl.rulesFor(org), org, project, service)

	// The /v1/gateway per-org OrgRPM is the runtime operator override. It binds
	// when set and tighter than (or in the absence of) any commerce rule —
	// most-restrictive-wins, in an org-scoped bucket namespace distinct from the
	// commerce scope keys so the two never share a bucket. gp is nil-safe.
	if orpm := rl.gp.OrgRPM(org); orpm > 0 && (rpm <= 0 || orpm < rpm) {
		key, rpm = "gwpolicy|"+org, orpm
	}

	// The static floor for this service class, in its own bucket namespace so it
	// can never share a bucket with a commerce scope or a gateway override. It
	// binds only when nothing else did: a configured rule is a DECISION about
	// this org and outranks a default, including a looser one.
	if rpm <= 0 {
		if d := defaultServiceRPM[service]; d > 0 {
			key, rpm = "default|"+org+"|"+service, d
		}
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
// commerce blip neither blocks traffic nor hammers commerce. It takes no context:
// the fetch is DETACHED and bounded on its own, because the entry it writes is
// shared and a client disconnect must not poison it for every later request.
//
// It ASKS the process that owns the rows, over the plane. It used to GET
// /v1/billing/alerts through the commerce transport, which dispatches by
// publishing the WHOLE shared app — so the fetch re-ran this very middleware,
// whose cache is filled only AFTER the fetch returns and is therefore still
// cold, which fetched again, to the transport's depth guard: 502. The plane
// socket carries this app's ops and no edge chain, so nothing it reaches can
// re-enter here.
func (rl *scopeRateLimiter) rulesFor(org string) []metering.ScopeRule {
	if !billingEnabled(rl.m) {
		return nil // no commerce configured — only the /v1/gateway OrgRPM applies.
	}
	rl.mu.Lock()
	e, ok := rl.cache[org]
	rl.mu.Unlock()
	if ok && time.Now().Before(e.expiry) {
		return e.rules
	}

	// The org is STATED: this runs on a detached context (a client disconnect must
	// not poison the cache), so there is no request for the callee to read it from.
	ctx, cancel := context.WithTimeout(For(context.Background(), org), scopeRulesTimeout)
	defer cancel()
	var rules []metering.ScopeRule
	if out, err := commerce.FinanceScopeRules(ctx); err == nil && out != nil {
		rules = make([]metering.ScopeRule, 0, len(out.Rules))
		for _, r := range out.Rules {
			rules = append(rules, metering.ScopeRule{Project: r.Project, Service: r.Service, RateLimitRpm: r.RateLimitRpm})
		}
	} // any error, or a commerce that is not deployed here, fails OPEN.

	rl.mu.Lock()
	rl.cache[org] = scopeCacheEntry{rules: rules, expiry: time.Now().Add(rl.ttl)}
	rl.mu.Unlock()
	return rules
}
