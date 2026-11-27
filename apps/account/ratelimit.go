package account

// Per-IP rate limiting for the abuse-sensitive console write routes (API key
// mint/rotate/revoke, HUSD wallet top-up). This is DISTINCT from commerce's spend-cap
// (ScopeRateLimit): it caps request FREQUENCY per client IP to blunt brute-force /
// enumeration / resource-exhaustion, restoring the edge protection cloud loses when a
// caller reaches it DIRECTLY (bypassing the gateway/ingress limiter) on the money path.
//
// A person is a token bucket: perMin tokens refilled continuously, as many as perMin
// at once. A caller with no principal is counted by address on a cloud.Window
// instead — the evicting count kms and event share — because an address is a key
// the caller mints, and a window it has stopped using is gone when the window ends.
// Both are counted across every pod the deployment runs (sdk/coord), so a
// caller spread over pods is held to one limit, not one per pod.
import (
	"context"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/geo"
	"github.com/zap-proto/zip"
)

type rateLimiter struct {
	perMin int
	people *coord.Coord  // signed-in callers, a bucket per person
	anon   *cloud.Window // callers with no principal, by address
}

// newRateLimiter builds a limiter allowing perMin requests a minute per caller.
// perMin<=0 disables limiting (allow always).
func newRateLimiter(perMin int) *rateLimiter {
	people := coord.Default()
	if !people.Shared() {
		people = coord.Alone() // this limiter's own buckets, apart from any other
	}
	return &rateLimiter{
		perMin: perMin,
		people: people,
		anon:   cloud.NewWindow("account:anon", perMin, time.Minute),
	}
}

// admit charges one request to the un-spoofable key for this caller. These are all
// POST-AUTH money-write routes, so the primary key is the VALIDATED principal
// (X-Org-Id/X-User-Id, minted by SanitizeIdentity from a verified JWT — a caller
// cannot forge it). An unauthenticated request (which the handler 403s anyway) has no
// principal, so it is counted by the caller's address: geo.IP, which the
// Window counts by an IPv6 caller's /64 and /48. Never c.Fiber().IP(): in this
// subsystem that is the host's socket, so every anonymous caller shared one bucket,
// and any one of them could spend it for the rest.
func (r *rateLimiter) admit(c *zip.Ctx) bool {
	if r.perMin <= 0 {
		return true
	}
	if uid := strings.TrimSpace(c.User()); uid != "" {
		// The person, whichever of their orgs they act in.
		return r.people.Take(context.Background(), "account:people", "p:"+uid, r.perMin)
	}
	return r.anon.Take(geo.IP(c))
}

// capped is THE frequency decision, once: nil when this caller may act again, a
// 429 when it may not. The middleware below and the ops' own preamble
// (requestCaller) are two ways to ASK it, never two copies of it.
func capped(rl *rateLimiter, c *zip.Ctx) error {
	if !rl.admit(c) {
		return zip.Errorf(429, "rate limit exceeded; retry shortly")
	}
	return nil
}

// rateLimit is that decision as MIDDLEWARE, for the untyped avatar write, which
// is a raw handler and has no preamble to put it in. It does NOT reach a typed
// op: With composes around the route's handler, and MCP, the call plane, the
// graph and the CLI call the op without ever building one — so the two key ops
// ask capped themselves and are not wrapped here, which is also what keeps one
// REST request costing one token.
func rateLimit(rl *rateLimiter) zip.Middleware {
	return func(next zip.Handler) zip.Handler {
		return func(c *zip.Ctx) error {
			if err := capped(rl, c); err != nil {
				return err
			}
			return next(c)
		}
	}
}
