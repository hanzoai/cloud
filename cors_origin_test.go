package cloud

// Tests for the CORS origin predicate (cors_origin.go) — the boundary that decides
// which browser origins may read this edge with credentials attached.
//
// They drive real requests through the zip/fiber stack wherever the answer is
// observable on the wire, because the thing under test is a header contract, and
// they drive the predicate directly where the point is a rule rather than a header.

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/zap-proto/zip"
)

// provenSet is a PROVEN source over a fixed set of verified hosts, counting calls
// so a test can prove the guards ran BEFORE the lookup and that answers are cached.
type provenSet struct {
	hosts map[string]string // host → org
	calls atomic.Int64
}

func (p *provenSet) fn(_ context.Context, host string) (string, bool) {
	p.calls.Add(1)
	org, ok := p.hosts[host]
	return org, ok
}

// newCORSOriginsForTest builds the predicate over an explicit declared list, going
// through setDeclared so a test exercises the same atomic install the middleware
// uses when an operator retunes the live allowlist.
func newCORSOriginsForTest(declared []string, proven verifiedHostFn) *corsOrigins {
	o := &corsOrigins{proven: proven}
	o.setDeclared(newOriginMatcher(declared))
	return o
}

// ── provableHost: the total rules that run before any lookup ─────────────────

func TestProvableHost(t *testing.T) {
	cases := []struct {
		origin string
		want   string // "" ⇒ refused
		why    string
	}{
		{"https://console.acme.com", "console.acme.com", "the shipped feature: a customer's own console"},
		{"https://acme.co.uk", "acme.co.uk", "multi-label TLD"},

		{"http://console.acme.com", "", "cleartext: credentialed CORS must not put the token on the wire"},
		{"https://console.acme.com:8443", "", "a proof is about a NAME, not a port"},
		{"https://localhost", "", "not a public FQDN"},
		{"https://127.0.0.1", "", "IP literal is not a name anyone can prove by DNS-01"},
		{"https://[::1]", "", "IPv6 literal"},
		{"null", "", "the sandboxed-iframe origin"},
		{"", "", "absent"},
		{"garbage", "", "not a URL"},
		{"intranet", "", "a BARE PROJECT SLUG: site_hosts holds these verified by construction"},
		{"https://intranet", "", "a bare slug dressed as an origin is still not an FQDN"},
		{"https://console.acme.com/", "", "trailing slash is not a serialized origin"},
		{"https://console.acme.com/path", "", "a path is not a serialized origin"},
		{"https://console.acme.com?q=1", "", "a query is not a serialized origin"},
		{"https://console.acme.com#f", "", "a fragment is not a serialized origin"},
		{"https://user:pw@console.acme.com", "", "userinfo is not a serialized origin"},
		{"HTTPS://console.acme.com", "", "upper-case scheme is not canonical"},
		{"https://CONSOLE.acme.com", "", "upper-case host is a different string than the stored row"},
		{"https://console.acme.com.", "", "trailing root dot: same resolution, different origin"},
		{" https://console.acme.com", "", "padding"},
		{"https://console.acme.com\r\nX: y", "", "header smuggling"},
	}
	for _, tc := range cases {
		got, ok := provableHost(tc.origin)
		if tc.want == "" {
			if ok {
				t.Errorf("provableHost(%q) = %q, want refused — %s", tc.origin, got, tc.why)
			}
			continue
		}
		if !ok || got != tc.want {
			t.Errorf("provableHost(%q) = %q,%v want %q — %s", tc.origin, got, ok, tc.want, tc.why)
		}
	}
}

// TestProvableHostRunsBeforeLookup is the point of the guards: a string that could
// never be a proven name must not cost a store read. The bare-slug case is the one
// that matters — site_hosts holds every project's bare slug as a row that is
// ALWAYS status='verified', so a resolver that saw it would say yes.
func TestProvableHostRunsBeforeLookup(t *testing.T) {
	p := &provenSet{hosts: map[string]string{"intranet": "attacker-org", "console.acme.com": "acme"}}
	o := &corsOrigins{proven: p.fn}

	for _, origin := range []string{
		"https://intranet", "intranet", "http://console.acme.com",
		"https://console.acme.com:8443", "null", "https://127.0.0.1",
	} {
		if o.allowed(context.Background(), origin) {
			t.Fatalf("%q must not be allowed", origin)
		}
	}
	if n := p.calls.Load(); n != 0 {
		t.Fatalf("guards must reject before any lookup; the store was asked %d times", n)
	}
}

// ── the predicate over its two sources ──────────────────────────────────────

func TestAllowedProvenAndDeclared(t *testing.T) {
	p := &provenSet{hosts: map[string]string{"console.acme.com": "acme"}}
	o := newCORSOriginsForTest([]string{"*.hanzo.ai"}, p.fn)
	ctx := context.Background()

	if !o.allowed(ctx, "https://console.acme.com") {
		t.Fatal("a VERIFIED site host must be allowed — this is the shipped feature")
	}
	if !o.allowed(ctx, "https://console.hanzo.ai") {
		t.Fatal("a DECLARED origin must still be allowed")
	}
	if o.allowed(ctx, "https://unverified.acme.com") {
		t.Fatal("a host with no verified row must be refused")
	}
	if o.allowed(ctx, "https://evil.example") {
		t.Fatal("an arbitrary domain must be refused")
	}
}

// TestDeclaredNeverAsksTheStore: the declared allowlist must keep working when the
// projects app is unreachable, so it is answered without a lookup.
func TestDeclaredNeverAsksTheStore(t *testing.T) {
	p := &provenSet{}
	o := newCORSOriginsForTest([]string{"*.hanzo.ai"}, p.fn)
	if !o.allowed(context.Background(), "https://console.hanzo.ai") {
		t.Fatal("declared origin must be allowed")
	}
	if n := p.calls.Load(); n != 0 {
		t.Fatalf("a declared origin must not cost a store read; asked %d times", n)
	}
}

// TestUnresolvableSourceDenies: sites.VerifiedHost folds a resolver ERROR into
// found=false, so an unreachable projects app narrows CORS to the declared list. It
// must never open it, and a nil source must not panic.
func TestUnresolvableSourceDenies(t *testing.T) {
	o := newCORSOriginsForTest([]string{"*.hanzo.ai"}, nil)
	if o.allowed(context.Background(), "https://console.acme.com") {
		t.Fatal("no PROVEN source installed must deny, not open")
	}
	if !o.allowed(context.Background(), "https://console.hanzo.ai") {
		t.Fatal("the declared list must survive an unreachable store")
	}

	errSrc := &corsOrigins{proven: func(context.Context, string) (string, bool) { return "", false }}
	if errSrc.allowed(context.Background(), "https://console.acme.com") {
		t.Fatal("a source that cannot answer must deny")
	}
}

// TestVerifiedHostMustNameAnOrg: "verified" is only half the requirement — the row
// has to be bound to a real org, or there is no tenant the grant belongs to.
func TestVerifiedHostMustNameAnOrg(t *testing.T) {
	o := &corsOrigins{proven: func(context.Context, string) (string, bool) { return "", true }}
	if o.allowed(context.Background(), "https://console.acme.com") {
		t.Fatal("a verified row with no org must not be a CORS grant")
	}
}

// ── caching: bounded, and negative as well as positive ──────────────────────

func TestProvenAnswersAreCachedBothWays(t *testing.T) {
	p := &provenSet{hosts: map[string]string{"console.acme.com": "acme"}}
	o := &corsOrigins{proven: p.fn}
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if !o.allowed(ctx, "https://console.acme.com") {
			t.Fatal("verified host must stay allowed")
		}
		if o.allowed(ctx, "https://nope.acme.com") {
			t.Fatal("unverified host must stay refused")
		}
	}
	// One per distinct host. Caching the MISSES is what stops a forged Origin from
	// turning each inbound request into an internal one on the pre-auth path.
	if n := p.calls.Load(); n != 2 {
		t.Fatalf("lookups = %d, want 2 (one per host, both directions cached)", n)
	}
}

// TestProvenCacheIsBounded: the ATTACKER PICKS THE KEY, so the cache must not grow
// with the number of distinct forged origins.
func TestProvenCacheIsBounded(t *testing.T) {
	p := &provenSet{}
	o := &corsOrigins{proven: p.fn}
	ctx := context.Background()
	for i := 0; i < maxProvenCache*3; i++ {
		o.allowed(ctx, fmt.Sprintf("https://h%d.attacker.example", i))
	}
	o.mu.Lock()
	n := len(o.cache)
	o.mu.Unlock()
	if n > maxProvenCache {
		t.Fatalf("cache holds %d entries, cap is %d — unbounded growth is reachable pre-auth", n, maxProvenCache)
	}
}

// TestPredicateIsRaceFreeAcrossItsTwoReaders: the predicate has two readers on
// different goroutines — the edge middleware and, through CORSAllows, the ai filter
// — while an operator retuning the live allowlist recompiles it underneath them.
// Run with -race, this is what proves the shared instance is safe to share.
func TestPredicateIsRaceFreeAcrossItsTwoReaders(t *testing.T) {
	p := &provenSet{hosts: map[string]string{"console.acme.com": "acme"}}
	o := newCORSOriginsForTest([]string{"*.hanzo.ai"}, p.fn)
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := 0; n < 200; n++ {
				switch i % 3 {
				case 0: // the edge middleware recompiling a retuned allowlist
					o.setDeclared(newOriginMatcher([]string{fmt.Sprintf("*.h%d.example", n%4)}))
				case 1: // a declared/proven read
					o.allowed(ctx, "https://console.acme.com")
				default: // a miss, which writes the cache
					o.allowed(ctx, fmt.Sprintf("https://m%d.example", n))
				}
			}
		}(i)
	}
	wg.Wait()
}

// ── the wire contract ───────────────────────────────────────────────────────

// provenApp mounts EdgeCORS over an explicit declared list and PROVEN source.
func provenApp(t *testing.T, declared []string, proven verifiedHostFn) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{})
	app.Use(edgeCORS(staticPol(t, edge.Policy{CORSOrigins: declared}), proven))
	app.Get("/probe", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "1"}) })
	app.Post("/probe", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "1"}) })
	return app
}

func do(t *testing.T, app *zip.App, method, origin string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, "/probe", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	res, err := app.Test(req)
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	return res
}

// TestVerifiedHostGetsCredentialedCORS is the feature, end to end on the wire: a
// customer's forked console on their own proven domain can call this API.
func TestVerifiedHostGetsCredentialedCORS(t *testing.T) {
	p := &provenSet{hosts: map[string]string{"console.acme.com": "acme"}}
	app := provenApp(t, nil, p.fn) // NOTHING declared: the proof alone carries it.

	res := do(t, app, http.MethodGet, "https://console.acme.com")
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "https://console.acme.com" {
		t.Fatalf("ACAO = %q, want the reflected verified origin", got)
	}
	if got := res.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("ACAC = %q, want true", got)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got == "*" {
		t.Fatal("wildcard with credentials is invalid per the Fetch standard")
	}
}

// TestPreflightAndActualAgree is the defect this whole change exists to prevent:
// a browser told YES at the preflight and NO at the request, or the reverse. Both
// hang off one predicate, so they are asserted together for both verdicts.
func TestPreflightAndActualAgree(t *testing.T) {
	p := &provenSet{hosts: map[string]string{"console.acme.com": "acme"}}
	app := provenApp(t, []string{"*.hanzo.ai"}, p.fn)

	for _, tc := range []struct {
		origin string
		allow  bool
	}{
		{"https://console.acme.com", true}, // proven
		{"https://console.hanzo.ai", true}, // declared
		{"https://unverified.acme.com", false},
		{"https://evil.example", false},
		{"https://console.acme.com:8443", false},
		{"http://console.acme.com", false},
	} {
		pre := do(t, app, http.MethodOptions, tc.origin)
		act := do(t, app, http.MethodPost, tc.origin)

		preACAO := pre.Header.Get("Access-Control-Allow-Origin")
		actACAO := act.Header.Get("Access-Control-Allow-Origin")
		if (preACAO != "") != tc.allow || (actACAO != "") != tc.allow {
			t.Errorf("%s: preflight ACAO=%q actual ACAO=%q, want allow=%v",
				tc.origin, preACAO, actACAO, tc.allow)
		}
		if preACAO != actACAO {
			t.Errorf("%s: preflight and actual DISAGREE (%q vs %q)", tc.origin, preACAO, actACAO)
		}
		if tc.allow && pre.StatusCode != 204 {
			t.Errorf("%s: preflight status = %d, want 204", tc.origin, pre.StatusCode)
		}
	}
}

// TestVaryOriginOnEveryOriginDependentAnswer — including the DENIAL. A shared cache
// that keys on the URL alone would otherwise hand one tenant the answer computed
// for another, and the answer that carries no ACAO depends on Origin just as much
// as the one that does.
func TestVaryOriginOnEveryOriginDependentAnswer(t *testing.T) {
	p := &provenSet{hosts: map[string]string{"console.acme.com": "acme"}}
	app := provenApp(t, nil, p.fn)

	for _, tc := range []struct{ origin, what string }{
		{"https://console.acme.com", "allowed"},
		{"https://evil.example", "DENIED"},
	} {
		for _, m := range []string{http.MethodGet, http.MethodOptions} {
			res := do(t, app, m, tc.origin)
			if !strings.Contains(res.Header.Get("Vary"), "Origin") {
				t.Errorf("%s %s (%s): Vary = %q, must contain Origin",
					m, tc.origin, tc.what, res.Header.Get("Vary"))
			}
		}
	}

	// No Origin at all ⇒ the answer does not depend on one, so nothing is added.
	if v := do(t, app, http.MethodGet, "").Header.Get("Vary"); strings.Contains(v, "Origin") {
		t.Errorf("a request with no Origin must not be marked origin-dependent; Vary = %q", v)
	}
}

// TestVaryIsAppendedNotAssigned: the previous code used SetHeader, which OVERWRITES,
// so it fought middleware_markdown's `Vary: Accept` and whichever ran last silently
// erased the other's protection.
func TestVaryIsAppendedNotAssigned(t *testing.T) {
	p := &provenSet{hosts: map[string]string{"console.acme.com": "acme"}}
	app := zip.New(zip.Config{})
	app.Use(zip.H(func(c *zip.Ctx) error { c.SetHeader("Vary", "Accept"); return c.Continue() }))
	app.Use(edgeCORS(staticPol(t, edge.Policy{}), p.fn))
	app.Get("/probe", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "1"}) })

	v := do(t, app, http.MethodGet, "https://console.acme.com").Header.Get("Vary")
	if !strings.Contains(v, "Origin") || !strings.Contains(v, "Accept") {
		t.Fatalf("Vary = %q, want BOTH Accept and Origin — appending must not clobber", v)
	}
}

// TestUnprovenOriginIsNotReflected: reflecting the request Origin before it resolved
// to a verified record is the classic hole this design exists to close.
func TestUnprovenOriginIsNotReflected(t *testing.T) {
	app := provenApp(t, nil, (&provenSet{}).fn)
	for _, origin := range []string{
		"https://evil.example", "https://console.acme.com", "null",
		"https://hanzo.ai.evil.example",
	} {
		res := do(t, app, http.MethodGet, origin)
		if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("origin %q was reflected as %q with nothing proving it", origin, got)
		}
		if got := res.Header.Get("Access-Control-Allow-Credentials"); got != "" {
			t.Errorf("origin %q got credentials with nothing proving it", origin)
		}
	}
}

// TestCORSAllowsIsFailSecureBeforeWiring — apps/ai asks this to decide whether the
// one authority already answered. Before an edge exists there is no authority, and
// the answer has to be no.
func TestCORSAllowsIsFailSecureBeforeWiring(t *testing.T) {
	saved := corsPredicate.Load()
	t.Cleanup(func() { corsPredicate.Store(saved) })

	corsPredicate.Store(nil)
	if CORSAllows(context.Background(), "https://console.hanzo.ai") {
		t.Fatal("with no predicate built, nothing is allowed")
	}
}

// TestCORSAllowsSharesTheEdgeVerdict: the ai filter and the edge middleware must be
// the SAME predicate, or they can drift into disagreeing about one origin — which is
// exactly the two-authority defect being removed.
func TestCORSAllowsSharesTheEdgeVerdict(t *testing.T) {
	saved := corsPredicate.Load()
	t.Cleanup(func() { corsPredicate.Store(saved) })

	p := &provenSet{hosts: map[string]string{"console.acme.com": "acme"}}
	app := provenApp(t, []string{"*.hanzo.ai"}, p.fn)
	// Drive one request so the live allowlist is compiled onto the published instance.
	do(t, app, http.MethodGet, "https://console.hanzo.ai")

	ctx := context.Background()
	for _, tc := range []struct {
		origin string
		want   bool
	}{
		{"https://console.acme.com", true},
		{"https://console.hanzo.ai", true},
		{"https://evil.example", false},
		{"https://unverified.acme.com", false},
	} {
		if got := CORSAllows(ctx, tc.origin); got != tc.want {
			t.Errorf("CORSAllows(%q) = %v, want %v — the two layers disagree", tc.origin, got, tc.want)
		}
	}
}
