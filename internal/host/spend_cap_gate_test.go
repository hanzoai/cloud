package host

// Money-path tests for the issue #70 edge enforcement: the spend-cap 402, the
// unreadable-cap 503, the soft-warn header, and the per-scope rate limit — all
// driven end-to-end through the real zip stack against a fake commerce that
// controls each verdict: the balance over HTTP, the cap over the plane.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/zap-proto/zip"
)

// capCommerce answers the metering client's balance read. The cap verdict is
// asked over the plane (planetest.Commerce.Cap), where the process that owns the
// caps answers it.
//
// It also serves GET /v1/billing/alerts, which nothing may call any more: the
// rate rules come over the plane now (see middleware_ratelimit_plane_test.go),
// because that HTTP fetch was the in-process self-dispatch that 502'd. alertsHits
// stays here as the tripwire — a caller that goes back to the old wire trips it.
type capCommerce struct {
	balanceBody string // GET /v1/billing/balance (default funded).

	alertsHits atomic.Int32
}

// (i) Funded caller, but the scope's HARD cap is exceeded → 402 spend_cap_exceeded
// (DISTINCT from insufficient_balance), handler NOT run.
func TestBillingGate_SpendCapExceeded402(t *testing.T) {
	planetest.Serve(t).Cap(client.CapVerdict{Allow: false, Reason: "spend_cap", CapCents: 100, SpentCents: 100})
	srv := (&capCommerce{}).server(t)

	var ran atomic.Bool
	app := newGateApp(t, mustClient(t, srv.URL), &ran)

	resp := doReq(t, app)
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", resp.StatusCode)
	}
	if ran.Load() {
		t.Fatal("handler ran despite spend cap exceeded")
	}
	assertErrorCode(t, resp, "spend_cap_exceeded")
}

// A funded caller whose cap could not be read is NOT let through: the cap op
// failing (an enforced cap whose spend is unreadable answers exactly this) is a
// 503 spend_cap_unavailable, handler not run. The HTTP read this replaced answered
// 404 in the process that owns the caps and the gate let the spend through.
func TestBillingGate_SpendCapUnreadable503(t *testing.T) {
	planetest.Serve(t).CapFails(zip.Errorf(http.StatusBadGateway, "cap authorize: spend-cap: enforced cap's spend unreadable"))
	srv := (&capCommerce{}).server(t)

	var ran atomic.Bool
	app := newGateApp(t, mustClient(t, srv.URL), &ran)

	resp := doReq(t, app)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	if ran.Load() {
		t.Fatal("handler ran past a spend cap nobody could read")
	}
	assertErrorCode(t, resp, "spend_cap_unavailable")
}

// A funded org with no cap set is served: no cap row is the one reason a cap
// check passes, and it is answered without reading any spend.
func TestBillingGate_NoCapServes(t *testing.T) {
	peer := planetest.Serve(t)
	srv := (&capCommerce{}).server(t)

	var ran atomic.Bool
	app := newGateApp(t, mustClient(t, srv.URL), &ran)

	if resp := doReq(t, app); resp.StatusCode != http.StatusOK || !ran.Load() {
		t.Fatalf("status = %d ran = %v, want 200 and served", resp.StatusCode, ran.Load())
	}
	if asks := peer.CapAsks(); len(asks) != 1 {
		t.Fatalf("cap checks = %v, want exactly one", asks)
	}
}

// (ii) Funded, under cap but over the soft threshold → handler runs (200) and the
// response carries X-Spend-Warn with the utilization percent.
func TestBillingGate_SpendWarnHeader(t *testing.T) {
	planetest.Serve(t).Cap(client.CapVerdict{Allow: true, WarnPct: 90})
	srv := (&capCommerce{}).server(t)

	var ran atomic.Bool
	app := newGateApp(t, mustClient(t, srv.URL), &ran)

	resp := doReq(t, app)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !ran.Load() {
		t.Fatal("handler did not run on an allowed (warned) request")
	}
	if got := resp.Header.Get("X-Spend-Warn"); got != "90" {
		t.Fatalf("X-Spend-Warn = %q, want 90", got)
	}
}

// rateApp wires an app with ONLY the scope rate limiter in front of a handler.
func rateApp(t *testing.T, m *meter.Client) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{})
	app.Use(ScopeRateLimit(m, nil))
	app.Raw(http.MethodPost, "/v1/agent/run", func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})
	return app
}

func rateReq(t *testing.T, app *zip.App, org, project string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/agent/run", nil)
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "alice")
	if project != "" {
		req.Header.Set("X-Project-Id", project)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("Test request: %v", err)
	}
	return resp
}

// (iii) + (iv) Rate limit: a scope with rpm=2 admits 2 requests in the window then
// 429s the 3rd — and the limit is per (org, project, service): a different org
// and a different project are NOT throttled by org A / project P's rule.
//
// The rules arrive over the plane; the fake commerce is still up so the tripwire
// on its retired /v1/billing/alerts route can prove nothing fetched them by HTTP.
func TestScopeRateLimit_PerScope429AndIsolation(t *testing.T) {
	servePlaneRules(t, map[string][]client.ScopeRule{
		// org "hanzo": a 2 rpm cap scoped to project "P".
		"hanzo": {{Project: "P", RateLimitRpm: 2}},
		// org "other": no rules → unlimited.
	}, nil)
	fc := &capCommerce{}
	srv := fc.server(t)
	app := rateApp(t, mustClient(t, srv.URL))
	t.Cleanup(func() {
		if n := fc.alertsHits.Load(); n != 0 {
			t.Errorf("GET /v1/billing/alerts was called %d times — the rate rules must come over the plane, not by dispatching this app back into itself", n)
		}
	})

	// hanzo / project P: 2 pass, 3rd is 429.
	if code := rateReq(t, app, "hanzo", "P").StatusCode; code != 200 {
		t.Fatalf("P req1 = %d, want 200", code)
	}
	if code := rateReq(t, app, "hanzo", "P").StatusCode; code != 200 {
		t.Fatalf("P req2 = %d, want 200", code)
	}
	resp3 := rateReq(t, app, "hanzo", "P")
	if resp3.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("P req3 = %d, want 429", resp3.StatusCode)
	}
	if got := resp3.Header.Get("X-RateLimit-Limit"); got != "2" {
		t.Fatalf("X-RateLimit-Limit = %q, want 2", got)
	}
	if got := resp3.Header.Get("X-RateLimit-Remaining"); got != "0" {
		t.Fatalf("X-RateLimit-Remaining = %q, want 0", got)
	}
	// When to come back, in whole seconds within the one-minute window.
	if got, err := strconv.Atoi(resp3.Header.Get("Retry-After")); err != nil || got < 1 || got > 60 {
		t.Fatalf("Retry-After = %q, want 1..60 seconds", resp3.Header.Get("Retry-After"))
	}

	// Project isolation: project Q (same org) is NOT covered by P's rule.
	for i := range 5 {
		if code := rateReq(t, app, "hanzo", "Q").StatusCode; code != 200 {
			t.Fatalf("Q req%d = %d, want 200 (project P's limit must not gate Q)", i+1, code)
		}
	}

	// Org isolation: org "other" has no rules — never throttled by hanzo's rule.
	for i := range 5 {
		if code := rateReq(t, app, "other", "P").StatusCode; code != 200 {
			t.Fatalf("other req%d = %d, want 200 (org hanzo's limit must not gate org other)", i+1, code)
		}
	}
}

func (f *capCommerce) server(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/billing/balance", func(w http.ResponseWriter, r *http.Request) {
		body := f.balanceBody
		if body == "" {
			body = `{"available":100000}`
		}
		_, _ = io.WriteString(w, body)
	})
	mux.HandleFunc("/v1/billing/alerts", func(w http.ResponseWriter, r *http.Request) {
		f.alertsHits.Add(1)
		_, _ = io.WriteString(w, `[]`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}
