package host

// Tests for the CORS origin predicate (cors_origin.go): every web origin may read
// the API, and only a first-party origin may read it with credentials.
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
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/zap-proto/zip"
)

// newCORSOriginsForTest builds the predicate over an explicit declared list, going
// through setDeclared so a test exercises the same atomic install the middleware
// uses when an operator retunes the live allowlist.
func newCORSOriginsForTest(declared []string) *corsOrigins {
	o := &corsOrigins{}
	o.setDeclared(newOriginMatcher(declared))
	return o
}

// TestWebOrigin: what may be echoed back at all. Exactly scheme://host[:port], so
// the reflected value is always its own canonical serialization.
func TestWebOrigin(t *testing.T) {
	for _, tc := range []struct {
		origin string
		want   bool
		why    string
	}{
		{"https://console.acme.com", true, "a customer's own site"},
		{"http://localhost:3000", true, "a developer's machine"},
		{"http://127.0.0.1:5173", true, "a loopback dev server"},
		{"https://acme.co.uk", true, "multi-label TLD"},
		{"https://acme-store.hanzo.app", true, "a site published under hanzo.app"},

		{"null", false, "a sandboxed frame or a file names no site"},
		{"", false, "absent"},
		{"garbage", false, "not a URL"},
		{"intranet", false, "a bare label"},
		{"ftp://acme.com", false, "not a web scheme"},
		{"https://console.acme.com/", false, "trailing slash is not a serialized origin"},
		{"https://console.acme.com/path", false, "a path is not a serialized origin"},
		{"https://console.acme.com?q=1", false, "a query is not a serialized origin"},
		{"https://console.acme.com#f", false, "a fragment is not a serialized origin"},
		{"https://user:pw@console.acme.com", false, "userinfo is not a serialized origin"},
		{"HTTPS://console.acme.com", false, "upper-case scheme is not canonical"},
		{" https://console.acme.com", false, "padding"},
		{"https://console.acme.com\r\nX: y", false, "header smuggling"},
	} {
		if got := webOrigin(tc.origin); got != tc.want {
			t.Errorf("webOrigin(%q) = %v, want %v — %s", tc.origin, got, tc.want, tc.why)
		}
	}
}

// TestSecureHost: what may carry credentials, before the registry is asked.
func TestSecureHost(t *testing.T) {
	for _, tc := range []struct {
		origin string
		want   string // "" ⇒ refused
		why    string
	}{
		{"https://hanzo.ai", "hanzo.ai", "an apex"},
		{"https://console.hanzo.ai", "console.hanzo.ai", "a subdomain"},

		{"http://hanzo.ai", "", "cleartext: a credential must not ride the wire"},
		{"https://hanzo.ai:8443", "", "another port is another origin"},
		{"https://localhost", "", "not a public name"},
		{"https://127.0.0.1", "", "IP literal"},
		{"https://[::1]", "", "IPv6 literal"},
		{"https://intranet", "", "a bare label"},
		{"https://CONSOLE.hanzo.ai", "", "upper-case host is a different string"},
		{"https://hanzo.ai.", "", "trailing root dot: same resolution, different origin"},
		{"https://hanzo.ai/", "", "trailing slash"},
		{"null", "", "no site"},
	} {
		got, ok := secureHost(tc.origin)
		if tc.want == "" {
			if ok {
				t.Errorf("secureHost(%q) = %q, want refused — %s", tc.origin, got, tc.why)
			}
			continue
		}
		if !ok || got != tc.want {
			t.Errorf("secureHost(%q) = %q,%v want %q — %s", tc.origin, got, ok, tc.want, tc.why)
		}
	}
}

func TestDeclaredIsCredentialed(t *testing.T) {
	o := newCORSOriginsForTest([]string{"*.partner.example", "http://localhost:3000"})
	ctx := context.Background()
	for _, origin := range []string{"https://app.partner.example", "https://partner.example", "http://localhost:3000"} {
		if got := o.admit(ctx, origin); got != corsCredentialed {
			t.Errorf("admit(%q) = %v, a declared origin is credentialed", origin, got)
		}
	}
	if got := o.admit(ctx, "http://localhost:4000"); got != corsAnonymous {
		t.Errorf("admit(http://localhost:4000) = %v, an undeclared dev origin reads anonymously", got)
	}
}

// TestPredicateIsRaceFreeAcrossItsTwoReaders: the predicate has two readers on
// different goroutines — the edge middleware and, through CORSAllows, the ai filter
// — while an operator retuning the live allowlist recompiles it underneath them.
// Run with -race, this is what proves the shared instance is safe to share.
func TestPredicateIsRaceFreeAcrossItsTwoReaders(t *testing.T) {
	o := newCORSOriginsForTest([]string{"*.hanzo.ai"})
	ctx := context.Background()

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for n := range 200 {
				switch i % 3 {
				case 0: // the edge middleware recompiling a retuned allowlist
					o.setDeclared(newOriginMatcher([]string{fmt.Sprintf("*.h%d.example", n%4)}))
				case 1:
					o.allowed(ctx, "https://console.hanzo.ai")
				default:
					o.allowed(ctx, fmt.Sprintf("https://m%d.example", n))
				}
			}
		}(i)
	}
	wg.Wait()

	// The last write wins and both readers see it: the predicate was not left
	// holding a matcher from the middle of the retune.
	o.setDeclared(newOriginMatcher([]string{"*.hanzo.ai"}))
	if got := o.admit(ctx, "https://console.hanzo.ai"); got != corsCredentialed {
		t.Fatalf("after the retune a declared origin reads %v, want credentialed", got)
	}
	if got := o.admit(ctx, "https://m1.h1.example"); got == corsCredentialed {
		t.Fatal("an origin from a replaced allowlist is still credentialed")
	}
}

func do(t *testing.T, app *zip.App, method, origin string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, "/probe", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	// An OPTIONS with an Origin is a PREFLIGHT here, and a preflight is defined by
	// carrying this (Fetch). Without it the request is the RFC 9110 §9.3.7
	// question instead — a different question, answered by a different handler.
	if method == http.MethodOptions && origin != "" {
		req.Header.Set("Access-Control-Request-Method", http.MethodPost)
	}
	res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	return res
}

// TestCORSClasses is the whole policy in one table: first-party origins are
// credentialed, every other web origin reads anonymously, and only a string that
// is not an origin is refused. A lookalike of a first-party host reads, and never
// with credentials. The preflight reflects exactly what the browser asked.
func TestCORSClasses(t *testing.T) {
	app := corsApp(t, nil)
	ask := "authorization, content-type, x-csrftoken, x-insights-session-id"
	for _, tc := range []struct {
		origin string
		want   corsClass
	}{
		{"https://cal.hanzo.ai", corsCredentialed},
		{"https://hanzo.ai", corsCredentialed},
		{"https://hanzo.team", corsCredentialed},
		{"https://hanzo.chat", corsCredentialed},
		{"https://hanzo.bot", corsCredentialed},
		{"https://hanzo.app", corsCredentialed},
		{"https://intel.hanzo.app", corsCredentialed},
		{"https://console.lux.cloud", corsCredentialed},
		{"https://lux.exchange", corsCredentialed},
		{"https://acme-store.hanzo.app", corsAnonymous},
		{"https://console.acme.com", corsAnonymous},
		{"https://shop.customer.example", corsAnonymous},
		{"http://localhost:3000", corsAnonymous},
		{"https://hanzo.ai.evil.example", corsAnonymous},
		{"https://evilhanzo.ai", corsAnonymous},
		{"http://hanzo.ai", corsAnonymous},
		{"https://hanzo.ai:8443", corsAnonymous},
		{"null", corsRefused},
		{"https://hanzo.ai/", corsRefused},
	} {
		req := httptest.NewRequest(http.MethodOptions, "/v1/event", nil)
		req.Header.Set("Origin", tc.origin)
		req.Header.Set("Access-Control-Request-Method", http.MethodPost)
		req.Header.Set("Access-Control-Request-Headers", ask)
		res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatalf("%s: %v", tc.origin, err)
		}
		acao := res.Header.Get("Access-Control-Allow-Origin")
		acac := res.Header.Get("Access-Control-Allow-Credentials")
		acah := res.Header.Get("Access-Control-Allow-Headers")
		switch tc.want {
		case corsRefused:
			if acao != "" || acac != "" || acah != "" {
				t.Errorf("%s: refused origin got ACAO=%q ACAC=%q ACAH=%q", tc.origin, acao, acac, acah)
			}
		default:
			if acao != tc.origin {
				t.Errorf("%s: ACAO = %q, want the origin reflected", tc.origin, acao)
			}
			if (acac == "true") != (tc.want == corsCredentialed) {
				t.Errorf("%s: ACAC = %q, credentialed = %v", tc.origin, acac, tc.want == corsCredentialed)
			}
			if acah != ask {
				t.Errorf("%s: ACAH = %q, want the asked headers %q", tc.origin, acah, ask)
			}
			if res.Header.Get("Access-Control-Max-Age") != corsMaxAge {
				t.Errorf("%s: preflight is not cached (Max-Age %q)", tc.origin, res.Header.Get("Access-Control-Max-Age"))
			}
		}
	}
}

// TestPreflightAndActualAgree: a browser told YES at the preflight and NO at the
// request, or the reverse, is the defect one predicate exists to prevent.
func TestPreflightAndActualAgree(t *testing.T) {
	app := corsApp(t, []string{"*.partner.example"})

	for _, tc := range []struct {
		origin string
		allow  bool
	}{
		{"https://console.acme.com", true},
		{"https://app.partner.example", true},
		{"https://console.hanzo.ai", true},
		{"http://localhost:5173", true},
		{"null", false},
		{"https://console.acme.com/x", false},
	} {
		pre := do(t, app, http.MethodOptions, tc.origin)
		act := do(t, app, http.MethodPost, tc.origin)

		preACAO := pre.Header.Get("Access-Control-Allow-Origin")
		actACAO := act.Header.Get("Access-Control-Allow-Origin")
		if (preACAO != "") != tc.allow || (actACAO != "") != tc.allow {
			t.Errorf("%s: preflight ACAO=%q actual ACAO=%q, want allow=%v", tc.origin, preACAO, actACAO, tc.allow)
		}
		if preACAO != actACAO {
			t.Errorf("%s: preflight and actual DISAGREE (%q vs %q)", tc.origin, preACAO, actACAO)
		}
		if pre.Header.Get("Access-Control-Allow-Credentials") != act.Header.Get("Access-Control-Allow-Credentials") {
			t.Errorf("%s: preflight and actual disagree on credentials", tc.origin)
		}
		if tc.allow && pre.StatusCode != 204 {
			t.Errorf("%s: preflight status = %d, want 204", tc.origin, pre.StatusCode)
		}
	}
}

// TestNoCredentialsBeyondFirstParty: reading is open, the visitor's session is
// not. No origin outside the declared list and the brand registry is ever told it
// may send credentials, on the preflight or the request.
func TestNoCredentialsBeyondFirstParty(t *testing.T) {
	app := corsApp(t, nil)
	for _, origin := range []string{
		"https://evil.example", "https://console.acme.com", "https://hanzo.ai.evil.example",
		"http://hanzo.ai", "http://localhost:3000", "https://acme-store.hanzo.app",
	} {
		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodOptions} {
			res := do(t, app, m, origin)
			if got := res.Header.Get("Access-Control-Allow-Credentials"); got != "" {
				t.Errorf("%s %s: ACAC = %q, only a first-party origin is credentialed", m, origin, got)
			}
			if got := res.Header.Get("Access-Control-Allow-Origin"); got != origin {
				t.Errorf("%s %s: ACAO = %q, every web origin may read", m, origin, got)
			}
		}
	}
}

// TestVaryOriginOnEveryOriginDependentAnswer — including the refusal. A shared
// cache that keys on the URL alone would otherwise hand one origin the answer
// computed for another.
func TestVaryOriginOnEveryOriginDependentAnswer(t *testing.T) {
	app := corsApp(t, nil)

	for _, tc := range []struct{ origin, what string }{
		{"https://console.acme.com", "anonymous"},
		{"https://hanzo.ai", "credentialed"},
		{"null", "refused"},
	} {
		for _, m := range []string{http.MethodGet, http.MethodOptions} {
			res := do(t, app, m, tc.origin)
			if !strings.Contains(res.Header.Get("Vary"), "Origin") {
				t.Errorf("%s %s (%s): Vary = %q, must contain Origin", m, tc.origin, tc.what, res.Header.Get("Vary"))
			}
		}
	}

	// No Origin at all ⇒ the answer does not depend on one, so nothing is added.
	if v := do(t, app, http.MethodGet, "").Header.Get("Vary"); strings.Contains(v, "Origin") {
		t.Errorf("a request with no Origin must not be marked origin-dependent; Vary = %q", v)
	}
}

// TestVaryIsAppendedNotAssigned: SetHeader OVERWRITES, so it fought
// middleware_markdown's `Vary: Accept` and whichever ran last erased the other.
func TestVaryIsAppendedNotAssigned(t *testing.T) {
	app := zip.New(zip.Config{})
	app.Use(zip.H(func(c *zip.Ctx) error { c.SetHeader("Vary", "Accept"); return c.Continue() }))
	app.Use(EdgeCORS(staticPol(t, edge.Policy{}), nil))
	app.Raw(http.MethodGet, "/probe", func(c *zip.Ctx) error { return c.JSON(200, map[string]string{"ok": "1"}) })

	v := do(t, app, http.MethodGet, "https://console.acme.com").Header.Get("Vary")
	if !strings.Contains(v, "Origin") || !strings.Contains(v, "Accept") {
		t.Fatalf("Vary = %q, want BOTH Accept and Origin — appending must not clobber", v)
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
// the SAME predicate, or they drift into disagreeing about one origin.
func TestCORSAllowsSharesTheEdgeVerdict(t *testing.T) {
	saved := corsPredicate.Load()
	t.Cleanup(func() { corsPredicate.Store(saved) })

	app := corsApp(t, []string{"*.partner.example"})
	// Drive one request so the live allowlist is compiled onto the published instance.
	do(t, app, http.MethodGet, "https://console.hanzo.ai")

	ctx := context.Background()
	for _, tc := range []struct {
		origin string
		want   bool
	}{
		{"https://console.acme.com", true},
		{"https://console.hanzo.ai", true},
		{"https://app.partner.example", true},
		{"http://localhost:3000", true},
		{"null", false},
	} {
		if got := CORSAllows(ctx, tc.origin); got != tc.want {
			t.Errorf("CORSAllows(%q) = %v, want %v — the two layers disagree", tc.origin, got, tc.want)
		}
	}
}
