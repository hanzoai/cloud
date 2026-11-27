package host

// Tests for the edge policy middleware (middleware_edge.go): browser CORS and the
// per-client-IP flood cap. They drive real requests through the zip/fiber stack,
// so the whole path runs end-to-end (header set, preflight short-circuit, 429).

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/zap-proto/zip"
)

// staticPol builds a static-only (no SQLite) policy store for the middleware tests.
func staticPol(t *testing.T, p edge.Policy) *edge.Store {
	t.Helper()
	s, _ := edge.New("", "admin", p) // "" dataDir ⇒ static-only, no file.
	return s
}

func TestOriginMatcher(t *testing.T) {
	if newOriginMatcher(nil) != nil {
		t.Fatal("empty allowlist must yield a nil matcher (CORS owned elsewhere)")
	}
	m := newOriginMatcher([]string{"https://app.example.com", "hanzo.chat", "*.hanzo.ai"})
	if m == nil {
		t.Fatal("non-empty allowlist must compile")
	}
	cases := []struct {
		origin string
		want   bool
	}{
		{"https://app.example.com", true},        // exact origin
		{"http://app.example.com", false},        // exact is scheme-specific
		{"https://hanzo.chat", true},             // bare host over https
		{"http://hanzo.chat", false},             // bare host never admits plain http
		{"https://hanzo.chat:8443", false},       // nor another port
		{"https://hanzo.ai", true},               // wildcard apex
		{"https://console.hanzo.ai", true},       // wildcard subdomain
		{"http://console.hanzo.ai", false},       // wildcard never admits plain http
		{"https://console.hanzo.ai:444", false},  // nor another port
		{"https://console.hanzo.ai/path", false}, // an origin has no path
		{"https://a.b.hanzo.ai", true},           // wildcard nested subdomain
		{"https://evil.com", false},              // not listed
		{"https://nothanzo.ai", false},           // suffix must be dot-bounded
		{"https://hanzo.ai.evil.com", false},
		{"garbage", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := m.allowed(tc.origin); got != tc.want {
			t.Errorf("allowed(%q) = %v, want %v", tc.origin, got, tc.want)
		}
	}
}

// corsApp mounts EdgeCORS for the given origins with a trivial /probe handler.
func corsApp(t *testing.T, origins []string) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{})
	app.Use(EdgeCORS(staticPol(t, edge.Policy{CORSOrigins: origins}), nil))
	app.Raw(http.MethodGet, "/probe", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "1"}) })
	app.Raw(http.MethodPost, "/probe", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "1"}) })
	return app
}

// TestEdgeCORS_DeclaresNothingServesEveryOrigin: with an empty declared allowlist
// a brand site is credentialed (derived from the brand registry) and any other
// site, a customer's, still reads.
func TestEdgeCORS_DeclaresNothingServesEveryOrigin(t *testing.T) {
	app := corsApp(t, nil)
	for origin, want := range map[string]string{
		"https://hanzo.ai":          "https://hanzo.ai",
		"https://shop.customer.com": "https://shop.customer.com",
		"null":                      "",
	} {
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.Header.Set("Origin", origin)
		res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("test: %v", err)
		}
		if got := res.Header.Get("Access-Control-Allow-Origin"); got != want {
			t.Fatalf("ACAO for %s = %q, want %q", origin, got, want)
		}
		if res.StatusCode != 200 {
			t.Fatalf("status = %d, want 200", res.StatusCode)
		}
	}
}

func TestEdgeCORS_ReflectsAllowlistedOrigin(t *testing.T) {
	app := corsApp(t, []string{"*.hanzo.ai"})
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Origin", "https://console.hanzo.ai")
	res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "https://console.hanzo.ai" {
		t.Fatalf("ACAO = %q, want reflected origin", got)
	}
	if got := res.Header.Get("Access-Control-Allow-Credentials"); got != "true" {
		t.Fatalf("ACAC = %q, want true", got)
	}
	// A browser SDK reads a 429's wait, a 401's scheme, an x402 challenge or
	// receipt, and the usage policy's word on a reply only if they are exposed.
	for _, h := range []string{"Retry-After", "WWW-Authenticate", "X-RateLimit-Remaining", "PAYMENT-REQUIRED", "PAYMENT-RESPONSE",
		"X-Hanzo-Usage,", "X-Hanzo-Usage-Class", "X-Hanzo-Paid-By", "X-Hanzo-Fallback", "X-Hanzo-Usage-Reason",
		"X-Hanzo-Usage-Resets", "X-Hanzo-Topup-Url", "X-Hanzo-Upgrade-Url",
		"x-ratelimit-limit-requests", "x-ratelimit-remaining-requests", "x-ratelimit-reset-requests"} {
		if got := res.Header.Get("Access-Control-Expose-Headers"); !strings.Contains(got, h) {
			t.Fatalf("Access-Control-Expose-Headers = %q, want it to name %s", got, h)
		}
	}
	if got := res.Header.Get("Vary"); got != "Origin" {
		t.Fatalf("Vary = %q, want Origin", got)
	}
	if res.StatusCode != 200 {
		t.Fatalf("actual request must still reach the handler (200), got %d", res.StatusCode)
	}
}

func TestEdgeCORS_PreflightShortCircuits(t *testing.T) {
	reached := false
	app := zip.New(zip.Config{})
	app.Use(EdgeCORS(staticPol(t, edge.Policy{CORSOrigins: []string{"*.hanzo.ai"}}), nil))
	app.Use(zip.H(func(c *zip.Ctx) error { reached = true; return c.Continue() }))
	app.Raw(http.MethodGet, "/probe", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "1"}) })

	req := httptest.NewRequest(http.MethodOptions, "/probe", nil)
	req.Header.Set("Origin", "https://hanzo.ai")
	// A preflight is DEFINED by this header (Fetch, CORS preflight request), and
	// it is what tells the edge apart from a bare OPTIONS — the RFC 9110 §9.3.7
	// question, which the contract's own endpoint answers with Allow. Sending it is
	// what makes this a model of a preflight rather than of something no browser
	// emits.
	req.Header.Set("Access-Control-Request-Method", "GET")
	req.Header.Set("Access-Control-Request-Headers", "content-type, authorization")
	res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.StatusCode != 204 {
		t.Fatalf("preflight status = %d, want 204", res.StatusCode)
	}
	if reached {
		t.Fatal("preflight must short-circuit BEFORE the rest of the chain (no auth work)")
	}
	if got := res.Header.Get("Access-Control-Allow-Methods"); got != corsAllowMethods {
		t.Fatalf("ACA-Methods = %q, want %q", got, corsAllowMethods)
	}
	if got := res.Header.Get("Access-Control-Allow-Headers"); got != "content-type, authorization" {
		t.Fatalf("ACA-Headers = %q, want the asked-for names", got)
	}
	if got := res.Header.Get("Access-Control-Max-Age"); got != corsMaxAge {
		t.Fatalf("ACA-Max-Age = %q, want %q", got, corsMaxAge)
	}
}

// TestEdgeCORS_PreflightAnswersAnyHeaderAsked is the regression for the defect a
// literal allow-headers list guarantees: a client attaches a header nobody added,
// the preflight does not name it, and the browser kills the request with an opaque
// "TypeError: Failed to fetch" that never reaches a server log.
//
// X-Idempotency-Key is the one that cost real money. billing.hanzo.ai stamps it on
// POST /v1/billing/subscribe/card (lib/commerce-client.ts subscribeWithCard) and it
// was never added to the list, so NO paid subscription could leave the browser: the
// Square card nonce was minted and the request died at the preflight. The header
// itself is unremarkable — that is the point. Any of them must work.
func TestEdgeCORS_PreflightAnswersAnyHeaderAsked(t *testing.T) {
	app := corsApp(t, []string{"*.hanzo.ai"})
	for _, ask := range []string{
		"content-type,x-idempotency-key",          // the billing checkout, verbatim
		"authorization, x-csrf-token, x-actor-id", // what the console stamps
		"x-something-nobody-has-added-yet",
	} {
		req := httptest.NewRequest(http.MethodOptions, "/probe", nil)
		req.Header.Set("Origin", "https://billing.hanzo.ai")
		req.Header.Set("Access-Control-Request-Method", "POST")
		req.Header.Set("Access-Control-Request-Headers", ask)
		res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("test: %v", err)
		}
		// Header names match case-insensitively (Fetch), so compare the way a browser
		// does — otherwise the assertion would flag names that are in fact answered.
		allowed := res.Header.Get("Access-Control-Allow-Headers")
		for name := range strings.SplitSeq(ask, ",") {
			name = strings.TrimSpace(name)
			if !strings.Contains(strings.ToLower(allowed), strings.ToLower(name)) {
				t.Errorf("asked %q, allow-headers %q omits %q — the browser would block the call",
					ask, allowed, name)
			}
		}
	}
}

// TestCORSAllowHeaders_EchoesTokensOnly: the echo is a header VALUE, so a
// hand-written ask must not be able to fold anything into the response.
func TestCORSAllowHeaders_EchoesTokensOnly(t *testing.T) {
	cases := []struct{ ask, want string }{
		{"", ""},
		{"Content-Type", "Content-Type"},
		{" x-a , x-b ", "x-a, x-b"},
		{"x-a,,x-b", "x-a, x-b"},                   // an empty name is not a name
		{"x-ok\r\nX-Injected: 1", ""},              // CRLF makes the WHOLE name malformed
		{"x-ok, x-bad\r\nX-Injected: 1", "x-ok"},   // …and only the malformed one is dropped
		{"x-ok, bad header", "x-ok"},               // a space is not a tchar
		{"x-ok, \"quoted\"", "x-ok"},               // nor is a quote
		{"x-idempotency-key", "x-idempotency-key"}, // the header this defect was about
	}
	for _, tc := range cases {
		if got := corsAllowHeaders(tc.ask); got != tc.want {
			t.Errorf("corsAllowHeaders(%q) = %q, want %q", tc.ask, got, tc.want)
		}
	}
}

func TestEdgeCORS_UnknownOriginReadsWithoutCredentials(t *testing.T) {
	app := corsApp(t, []string{"*.hanzo.ai"})
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Origin", "https://evil.example")
	res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "https://evil.example" {
		t.Fatalf("any web origin reads; ACAO = %q", got)
	}
	if got := res.Header.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("an unknown origin must never be credentialed, got ACAC %q", got)
	}
}

// TestEdgeCORS_RefusedPreflightIsAnswered: a preflight this edge does not admit is
// still ANSWERED here, because this middleware is the only thing in the process
// that answers one. It gets 204 without Access-Control-Allow-Origin — the browser
// blocks on the missing header, which is the true reason and the one a developer
// can read. Continuing instead hands the preflight to a router with no OPTIONS
// route, and the answer becomes a 404 about an endpoint that exists.
//
// The refusal is the ABSENT header, never the status, so nothing about admission
// changes here: the negative half of that is TestEdgeCORS_DeclaresNothingServesEveryOrigin
// above, and TestPreflightAndActualAgree pins the two halves to one predicate. The
// one origin refused is one that names no site: `null`.
func TestEdgeCORS_RefusedPreflightIsAnswered(t *testing.T) {
	routed := false
	app := zip.New(zip.Config{})
	app.Use(EdgeCORS(staticPol(t, edge.Policy{CORSOrigins: []string{"*.hanzo.ai"}}), nil))
	app.Raw(zip.MethodAll, "/*", func(c *zip.Ctx) error { routed = true; return c.JSON(404, map[string]string{"e": "not found"}) })

	req := httptest.NewRequest(http.MethodOptions, "/v1/event", nil)
	req.Header.Set("Origin", "null")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.StatusCode != 204 {
		t.Fatalf("refused preflight status = %d, want 204 (a 404 names the wrong problem)", res.StatusCode)
	}
	if routed {
		t.Fatal("a preflight must never reach the router: it has no OPTIONS route to meet")
	}
	if got := res.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("refused preflight must carry no ACAO, got %q", got)
	}
	if got := res.Header.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Fatalf("refused preflight must carry no ACA-Credentials, got %q", got)
	}
	if got := res.Header.Get("Vary"); !strings.Contains(got, "Origin") {
		t.Fatalf("Vary = %q, want Origin: this answer depends on it", got)
	}
}

// rateApp mounts EdgeRateLimit with a small per-IP limit and a wide window so the
// window never rolls over mid-test.
func edgeRateApp(t *testing.T, limit int, enabled bool) *zip.App {
	perIP := limit
	if !enabled {
		perIP = 0 // disabled ⇒ no cap
	}
	app := zip.New(zip.Config{})
	app.Use(EdgeRateLimit(staticPol(t, edge.Policy{PerIPRPM: perIP, WindowSec: 60}), nil))
	app.Raw(http.MethodGet, "/probe", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "1"}) })
	return app
}

func edgeGet(t *testing.T, app *zip.App, xff string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	return res.StatusCode
}

func TestEdgeRateLimit_PerIP(t *testing.T) {
	app := edgeRateApp(t, 3, true)
	// 3 allowed, 4th over the limit for the same public IP.
	for i := 1; i <= 3; i++ {
		if code := edgeGet(t, app, "203.0.113.7"); code != 200 {
			t.Fatalf("request %d: status %d, want 200", i, code)
		}
	}
	if code := edgeGet(t, app, "203.0.113.7"); code != 429 {
		t.Fatalf("4th request must be 429, got %d", code)
	}
	// A DIFFERENT client IP is on its own bucket, unaffected.
	if code := edgeGet(t, app, "198.51.100.9"); code != 200 {
		t.Fatalf("distinct IP must not share a bucket, got %d", code)
	}
}

// A 429 says when to come back: Retry-After in whole seconds inside the window,
// and the same figure in the sentence for whoever reads the body.
func TestEdgeRateLimit_SaysWhenToComeBack(t *testing.T) {
	app := edgeRateApp(t, 1, true)
	if code := edgeGet(t, app, "203.0.113.8"); code != 200 {
		t.Fatalf("first request: status %d, want 200", code)
	}
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.8")
	res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 429 {
		t.Fatalf("second request: status %d, want 429", res.StatusCode)
	}
	wait, err := strconv.Atoi(res.Header.Get("Retry-After"))
	if err != nil || wait < 1 || wait > 60 {
		t.Fatalf("Retry-After = %q, want 1..60 seconds", res.Header.Get("Retry-After"))
	}
	if !strings.Contains(string(body), "retry after "+strconv.Itoa(wait)+"s") {
		t.Errorf("body %s does not say when to retry", body)
	}
}

func TestEdgeRateLimit_XFFChainUsesLeftmost(t *testing.T) {
	app := edgeRateApp(t, 2, true)
	// The client is the right-most entry that is not one of our hops; which of
	// our hops appended after it must not change the bucket identity.
	for i := 1; i <= 2; i++ {
		if code := edgeGet(t, app, "203.0.113.50, 10.0.0.1"); code != 200 {
			t.Fatalf("request %d: status %d, want 200", i, code)
		}
	}
	if code := edgeGet(t, app, "203.0.113.50, 10.0.0.2"); code != 429 {
		t.Fatalf("same leftmost client must share the bucket → 429, got %d", code)
	}
}

// One IPv6 host holds a /64 and can take a fresh address in it per request: every
// address in it is one bucket, and the next /64 over is somebody else. Through
// Cloudflare too, where the address is CF-Connecting-IP rather than the chain.
func TestEdgeRateLimit_IPv6CountsPerSlash64(t *testing.T) {
	app := edgeRateApp(t, 2, true)
	for i, addr := range []string{"2001:db8:1:2::1", "2001:db8:1:2::2"} {
		if code := edgeGet(t, app, addr); code != 200 {
			t.Fatalf("request %d from %s: status %d, want 200", i+1, addr, code)
		}
	}
	if code := edgeGet(t, app, "2001:db8:1:2:ffff:ffff:ffff:ffff"); code != 429 {
		t.Fatalf("a third address in the same /64 must share its bucket → 429, got %d", code)
	}
	if code := edgeGet(t, app, "2001:db8:1:3::1"); code != 200 {
		t.Fatalf("the neighbouring /64 must not share the bucket, got %d", code)
	}

	behindCloudflare := func(visitor string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/probe", nil)
		req.Header.Set("X-Forwarded-For", visitor+", 2606:4700:10::1")
		req.Header.Set("CF-Connecting-IP", visitor)
		res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("test: %v", err)
		}
		return res.StatusCode
	}
	for i, addr := range []string{"2001:db8:9:9::a", "2001:db8:9:9::b"} {
		if code := behindCloudflare(addr); code != 200 {
			t.Fatalf("Cloudflare request %d from %s: status %d, want 200", i+1, addr, code)
		}
	}
	if code := behindCloudflare("2001:db8:9:9::c"); code != 429 {
		t.Fatalf("a /64 rotating behind Cloudflare must share its bucket → 429, got %d", code)
	}
}

func TestEdgeRateLimit_InClusterExempt(t *testing.T) {
	app := edgeRateApp(t, 2, true)
	// No X-Forwarded-For ⇒ in-cluster direct caller ⇒ never limited.
	for i := 1; i <= 5; i++ {
		if code := edgeGet(t, app, ""); code != 200 {
			t.Fatalf("in-cluster request %d must be exempt, got %d", i, code)
		}
	}
}

func TestEdgeRateLimit_Disabled(t *testing.T) {
	app := edgeRateApp(t, 2, false)
	for i := 1; i <= 5; i++ {
		if code := edgeGet(t, app, "203.0.113.7"); code != 200 {
			t.Fatalf("disabled limiter must pass request %d, got %d", i, code)
		}
	}
}

// A /48 is sixteen callers at the edge, however many /64s it rotates through: the
// next /64 it has never sent is refused once the site has spent sixteen limits. One
// /64 hammering past its own limit spends nothing of the site, and the neighbouring
// /48 is somebody else.
func TestEdgeRateLimit_IPv6CountsPerSlash48(t *testing.T) {
	app := edgeRateApp(t, 2, true)
	for i := range 16 {
		for range 2 {
			if code := edgeGet(t, app, fmt.Sprintf("2001:470:1f00:%x::1", i)); code != 200 {
				t.Fatalf("/64 %d of 16 in one /48: status %d, want 200", i+1, code)
			}
		}
	}
	if code := edgeGet(t, app, "2001:470:1f00:ffff::1"); code != 429 {
		t.Fatalf("a 17th /64 of a /48 that spent sixteen limits must be refused → 429, got %d", code)
	}
	if code := edgeGet(t, app, "2001:470:1f01::1"); code != 200 {
		t.Fatalf("the neighbouring /48 must not share the site's bucket, got %d", code)
	}

	hammered := edgeRateApp(t, 2, true)
	for range 100 {
		edgeGet(t, hammered, "2001:470:2f00:1::1")
	}
	if code := edgeGet(t, hammered, "2001:470:2f00:2::1"); code != 200 {
		t.Fatalf("one /64's refusals closed its /48 for a neighbour, got %d", code)
	}
}

// A NEIGHBOUR FILLING A /48 REFUSES ONLY WHO PRESENTS NOTHING. Sixteen anonymous
// /64s spend the site; a fresh /64 presenting a credential identity could make a
// principal of — a bearer, a session cookie, an sk- key — is held only by the
// backstop on its own /64, ten times the limit, and served, while one presenting
// nothing identity reads — an unrelated cookie, a publishable key, a key on Api-Key
// or ?api_key= — is refused with the site. (What a credential that turns out to be nobody is charged
// is the identity boundary's half: middleware_edge_identity_test.go.)
func TestEdgeRateLimit_ACredentialIsNotHeldByItsSlash48(t *testing.T) {
	app := edgeRateApp(t, 2, true)
	for i := range 16 {
		for range 2 {
			edgeGet(t, app, fmt.Sprintf("2001:470:1f00:%x::1", i))
		}
	}
	call := func(addr, target string, set func(*http.Request)) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("X-Forwarded-For", addr)
		if set != nil {
			set(req)
		}
		res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("test: %v", err)
		}
		return res.StatusCode
	}
	header := func(k, v string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set(k, v) }
	}
	if code := call("2001:470:1f00:ff00::1", "/probe", nil); code != 429 {
		t.Fatalf("an anonymous /64 of a spent /48 answered %d, want 429; this test proves nothing", code)
	}
	for i, tc := range []struct {
		target string
		set    func(*http.Request)
	}{
		{"/probe", header("Authorization", "Bearer eyJhbGciOiJSUzI1NiJ9.e30.sig")},
		{"/probe", header("Authorization", "Bearer sk-live-0123456789abcdef")},
		{"/probe", header("X-Api-Key", "sk-live-0123456789abcdef")},
		{"/probe", header("Cookie", "hanzo_iam_token=eyJhbGciOiJSUzI1NiJ9.e30.sig")},
	} {
		addr := fmt.Sprintf("2001:470:1f00:ee%02x::1", i)
		for n := range 2 * edge.PresentedScale {
			if code := call(addr, tc.target, tc.set); code != 200 {
				t.Fatalf("credential %d request %d from a fresh /64 of a spent /48 answered %d, want 200 up to the backstop", i, n+1, code)
			}
		}
		if code := call(addr, tc.target, tc.set); code != 429 {
			t.Errorf("credential %d past its /64's backstop answered %d, want 429: the /64 still holds it", i, code)
		}
	}
	// What identity never makes a principal of is anonymous to the edge: an unrelated
	// cookie, a publishable key, a key on Api-Key or ?api_key=.
	for i, tc := range []struct {
		target string
		set    func(*http.Request)
	}{
		{"/probe", header("Cookie", "_ga=GA1.1.1")},
		{"/probe", header("Authorization", "Bearer pk-live-0123456789abcdef")},
		{"/probe", header("Api-Key", "sk-live-0123456789abcdef")},
		{"/probe?api_key=sk-live-0123456789abcdef", nil},
	} {
		if code := call(fmt.Sprintf("2001:470:1f00:ff%02x::1", i), tc.target, tc.set); code != 429 {
			t.Errorf("non-principal credential %d bought a way past the spent /48: %d, want 429", i, code)
		}
	}
}

// A refusal says when EVERY full bucket has room again. A /64 that refuses inside a
// /48 that is also full, and resets later, sends its caller back at the /48's reset;
// told the /64's, the caller returns to be refused by the /48.
func TestEdgeRateLimit_RetryAfterWaitsForTheSite(t *testing.T) {
	rl := &edgeIPLimiter{policy: staticPol(t, edge.Policy{PerIPRPM: 2, WindowSec: 60})}
	fullSite(&rl.window, "2001:470:1f00:1::1", 2, 5*time.Second, 50*time.Second)
	app := zip.New(zip.Config{})
	app.Use(zip.Handler(rl.handler))
	app.Raw(http.MethodGet, "/probe", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "1"}) })

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("X-Forwarded-For", "2001:470:1f00:1::1")
	res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	if res.StatusCode != 429 {
		t.Fatalf("status %d, want 429", res.StatusCode)
	}
	if wait, _ := strconv.Atoi(res.Header.Get("Retry-After")); wait < 49 {
		t.Fatalf("Retry-After = %d; the /48 is full for 50s, so the caller must wait for it", wait)
	}
}
