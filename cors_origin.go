package cloud

// The CORS origin predicate: may this browser origin read an answer from this
// edge, with the caller's credentials attached?
//
// It is ONE function (corsOrigins.allowed) over TWO sources, and the split is the
// whole design:
//
//	DECLARED — the platform allowlist (edge.Policy.CORSOrigins). The brand hosts an
//	           operator names. SuperAdmin-writable, evaluated before identity.
//	PROVEN   — a host whose site_hosts row is VERIFIED and bound to an org. The
//	           customer proved control of the name over DNS; nobody had to type it
//	           into a config.
//
// PROVEN is what makes the shipped product feature possible: a customer forks
// hanzoai/console, deploys it on their own domain, and it can call this API. Under
// a static list alone it could not, because the list can only ever name domains we
// own. It is not a new trust mechanism — it is the EXISTING one, asked a question:
//
//	site_hosts.status separates HOLDING a name from SERVING it. `pending` takes the
//	name against the primary key and carries a 128-bit DNS-01 challenge token;
//	`verified` is what a caller gets after fqdn.Verify finds that token published as
//	TXT at _hanzo-challenge.<host>. Store.ResolveHost filters status='verified' and
//	is the sole routing read — the hostname-hijack boundary. sites.VerifiedHost asks
//	exactly that, so a claim on a name the claimant does not own grants nothing here
//	either. One proof, one boundary, two readers.
//
// Everything below the two sources is guard rails on an ATTACKER-CONTROLLED string.
// The Origin header is chosen by whoever is calling, and the PROVEN lookup is a
// plane hop on the pre-auth path, so the cheap total rules run first and the
// expensive one runs last and is cached in both directions.

import (
	"context"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hanzoai/cloud/apps/sites"
	"github.com/hanzoai/cloud/internal/fqdn"
)

// verifiedHostFn is the PROVEN source. A client, not a config: the tests drive it
// directly, and production is sites.VerifiedHost, which prefers the in-process
// projects store and falls back to the plane. Production genuinely needs the
// fallback — the pod boots ~25 single-app processes, so the projects store is not
// co-resident with the edge that reads it.
type verifiedHostFn func(ctx context.Context, host string) (string, bool)

// corsOrigins answers the one question, over the two sources, with a bounded cache
// in front of the expensive one.
//
// declared is ATOMIC because this value has two readers on different goroutines —
// the edge middleware and, through CORSAllows, the ai filter — while the middleware
// recompiles it whenever an operator retunes the live allowlist. A plain field
// would be a write racing two reads on every policy change.
type corsOrigins struct {
	declared atomic.Pointer[originMatcher]
	proven   verifiedHostFn

	mu        sync.Mutex
	cache     map[string]provenEntry
	lastSweep time.Time
}

// setDeclared installs a freshly compiled allowlist.
func (o *corsOrigins) setDeclared(m *originMatcher) { o.declared.Store(m) }

// provenEntry caches a PROVEN answer in BOTH directions. Caching only the hits
// would leave every forged origin paying for a full plane hop, which turns one
// attacker request into one internal request — the pre-auth path is exactly where
// that must not be true.
type provenEntry struct {
	org    string
	ok     bool
	expiry time.Time
}

const (
	// provenTTL bounds how stale a PROVEN answer may be. Short, because it is how
	// long a released or newly-unverified domain keeps working; long enough that a
	// live console does not re-ask on every request. Mirrors edge.resolveTTL.
	provenTTL = 5 * time.Second
	// maxProvenCache bounds the cache. The ATTACKER PICKS THE KEY — every distinct
	// forged Origin is a distinct entry — so an unbounded map here is a memory
	// exhaustion primitive reachable before authentication.
	maxProvenCache = 4096
)

// allowed is the predicate. Both the preflight and the actual request are decided
// by this one call, so there is no way for them to disagree.
func (o *corsOrigins) allowed(ctx context.Context, origin string) bool {
	// An origin the operator DECLARED is admitted on its own terms, including the
	// wildcard and bare-host forms that allowlist has always used. Checked first: it
	// is a map lookup, it never touches the store, and it must keep working when the
	// projects app is unreachable.
	if d := o.declared.Load(); d != nil && d.allowed(origin) {
		return true
	}
	host, ok := provableHost(origin)
	if !ok {
		return false
	}
	return o.provenHost(ctx, host)
}

// provableHost reduces an Origin header to the hostname a DNS proof could be about,
// and reports false for every string that is not one. It is a TOTAL rule applied
// before any lookup, and each clause closes something specific:
//
//   - exactly `scheme://host`, reconstructed and compared — so a path, a query, a
//     fragment, userinfo, a trailing slash, an upper-case scheme, and (via url.Parse,
//     which refuses them) any embedded control character are misses rather than
//     near-hits. This is what makes echoing the header back safe: the only strings
//     that can reach the response already equal their own canonical serialization.
//   - https only. Access-Control-Allow-Credentials over cleartext puts the
//     credential on the wire, and control of a zone's DNS is not evidence that the
//     zone is served over TLS.
//   - no port. A proof is about a NAME. `https://proven.example.com:8443` is a
//     different origin and admitting it widens the reflected set for no product
//     gain — the feature is a console on 443.
//   - fqdn.Valid — the SAME syntactic rule the bind path applies, so this surface
//     can only ever address names that surface could have created. It is also what
//     keeps BARE PROJECT SLUGS out: site_hosts holds each project's bare slug as a
//     structural row, always status='verified', and a bare label is not a valid
//     FQDN. Without this a tenant holding the slug `intranet` would have made
//     `Origin: https://intranet` a credentialed origin. It rejects `null`, an IP
//     literal and `localhost` for the same reason.
func provableHost(origin string) (string, bool) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", false
	}
	if origin != u.Scheme+"://"+u.Host {
		return "", false
	}
	if u.Port() != "" {
		return "", false
	}
	host := u.Hostname()
	// fqdn.Valid expects Clean's output and does not normalize, so compare against
	// it rather than calling Clean: a host that is not ALREADY canonical (upper
	// case, a trailing root dot) is a different origin string and is refused here
	// instead of being silently folded onto a row it does not name.
	if host != fqdn.Clean(host) || !fqdn.Valid(host) {
		return "", false
	}
	return host, true
}

// provenHost asks the PROVEN source through the cache.
func (o *corsOrigins) provenHost(ctx context.Context, host string) bool {
	if o.proven == nil {
		return false
	}
	now := time.Now()

	o.mu.Lock()
	if e, hit := o.cache[host]; hit && now.Before(e.expiry) {
		o.mu.Unlock()
		return e.ok
	}
	o.mu.Unlock()

	// Resolved OUTSIDE the lock: this is a plane hop in production, and holding the
	// mutex across it would serialize every concurrent request behind the slowest
	// lookup. A duplicate in-flight lookup for the same host is cheaper than that.
	//
	// sites.VerifiedHost folds a resolver ERROR into found=false, so an unreachable
	// projects app narrows CORS to the DECLARED list. It never opens it, and it
	// never takes the edge down.
	org, ok := o.proven(ctx, host)

	o.mu.Lock()
	o.sweepLocked(now)
	if o.cache == nil {
		o.cache = map[string]provenEntry{}
	}
	o.cache[host] = provenEntry{org: org, ok: ok && org != "", expiry: now.Add(provenTTL)}
	o.mu.Unlock()
	return ok && org != ""
}

// sweepLocked keeps the cache bounded. Expired entries go first; if the map is
// still at the cap, it is dropped whole rather than grown. Dropping wholesale
// costs a re-resolve for the live consoles in it, which is bounded and self-heals
// within provenTTL — growing without bound does not. Caller holds o.mu.
func (o *corsOrigins) sweepLocked(now time.Time) {
	if len(o.cache) < maxProvenCache && now.Sub(o.lastSweep) < provenTTL {
		return
	}
	o.lastSweep = now
	for h, e := range o.cache {
		if now.After(e.expiry) {
			delete(o.cache, h)
		}
	}
	if len(o.cache) >= maxProvenCache {
		o.cache = map[string]provenEntry{}
	}
}

// originMatcher decides whether a request Origin is on the DECLARED allowlist. Each
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

// corsVerifiedHost is the production PROVEN source, named so the middleware reads
// as one word and the tests have something to substitute.
func corsVerifiedHost(ctx context.Context, host string) (string, bool) {
	return sites.VerifiedHost(ctx, host)
}
