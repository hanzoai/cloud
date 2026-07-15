package cloud

// Money-path tests for the issue #70 edge enforcement: the spend-cap 402, the
// soft-warn header, and the per-scope rate limit — all driven end-to-end through
// the real zip stack against a fake commerce that controls each verdict.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/clients/metering"
	"github.com/zap-proto/zip"
)

// capCommerce answers the metering client's balance, spend-cap authorize, and
// spend-alerts (rate-rules) calls. Each verdict is fully controlled per test.
type capCommerce struct {
	balanceBody string            // GET /v1/billing/balance (default funded).
	authorize   string            // GET /v1/billing/spend-alerts/authorize (the cap verdict).
	rulesFor    map[string]string // X-Org-Id -> GET /v1/billing/spend-alerts body.
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
	mux.HandleFunc("/v1/billing/spend-alerts/authorize", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, f.authorize)
	})
	mux.HandleFunc("/v1/billing/spend-alerts", func(w http.ResponseWriter, r *http.Request) {
		body := f.rulesFor[r.Header.Get("X-Org-Id")]
		if body == "" {
			body = `[]`
		}
		_, _ = io.WriteString(w, body)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// (i) Funded caller, but the scope's HARD cap is exceeded → 402 spend_cap_exceeded
// (DISTINCT from insufficient_balance), handler NOT run.
func TestBillingGate_SpendCapExceeded402(t *testing.T) {
	fc := &capCommerce{authorize: `{"allow":false,"reason":"spend_cap","capCents":100,"spentCents":100}`}
	srv := fc.server(t)

	var ran atomic.Bool
	app := newGateApp(t, mustClient(t, srv.URL, false), &ran)

	resp := doReq(t, app)
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", resp.StatusCode)
	}
	if ran.Load() {
		t.Fatal("handler ran despite spend cap exceeded")
	}
	assertErrorCode(t, resp, "spend_cap_exceeded")
}

// (ii) Funded, under cap but over the soft threshold → handler runs (200) and the
// response carries X-Spend-Warn with the utilization percent.
func TestBillingGate_SpendWarnHeader(t *testing.T) {
	fc := &capCommerce{authorize: `{"allow":true,"reason":"","warnPct":90}`}
	srv := fc.server(t)

	var ran atomic.Bool
	app := newGateApp(t, mustClient(t, srv.URL, false), &ran)

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

// (MED-4) A resource-path spend-cap denial renders the DISTINCT 402
// spend_cap_exceeded — mirroring the edge gate — never the 503 out-of-funds shape
// (wrong code + retry storm). One handler drives DenyResource with each error.
func TestDenyResource_SpendCapDistinct402(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
		wantBody string
	}{
		{"spend_cap", metering.ErrSpendCapExceeded, http.StatusPaymentRequired, "spend_cap_exceeded"},
		{"insufficient", metering.ErrInsufficientBalance, http.StatusPaymentRequired, "insufficient_balance"},
	}
	for _, tc := range cases {
		app := zip.New(zip.Config{})
		app.Post("/v1/x", func(c *zip.Ctx) error { return DenyResource(c, tc.err) })
		resp, err := app.Fiber().Test(httptest.NewRequest(http.MethodPost, "/v1/x", nil))
		if err != nil {
			t.Fatalf("%s: Test: %v", tc.name, err)
		}
		if resp.StatusCode != tc.wantCode {
			t.Fatalf("%s: status = %d, want %d", tc.name, resp.StatusCode, tc.wantCode)
		}
		body, _ := io.ReadAll(resp.Body)
		if !containsSub(string(body), `"code":"`+tc.wantBody+`"`) {
			t.Fatalf("%s: body %q missing %q", tc.name, string(body), tc.wantBody)
		}
	}
}

// rateApp wires an app with ONLY the scope rate limiter in front of a handler.
func rateApp(t *testing.T, m *metering.Client) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{})
	app.Use(ScopeRateLimit(m, nil))
	app.Post("/v1/agent/run", func(c *zip.Ctx) error {
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
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test request: %v", err)
	}
	return resp
}

// (iii) + (iv) Rate limit: a scope with rpm=2 admits 2 requests in the window then
// 429s the 3rd — and the limit is per (org, project, service): a different org
// and a different project are NOT throttled by org A / project P's rule.
func TestScopeRateLimit_PerScope429AndIsolation(t *testing.T) {
	fc := &capCommerce{rulesFor: map[string]string{
		// org "hanzo": a 2 rpm cap scoped to project "P".
		"hanzo": `[{"project":"P","service":"","rateLimitRpm":2}]`,
		// org "other": no rules → unlimited.
		"other": `[]`,
	}}
	srv := fc.server(t)
	app := rateApp(t, mustClient(t, srv.URL, false))

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

	// Project isolation: project Q (same org) is NOT covered by P's rule.
	for i := 0; i < 5; i++ {
		if code := rateReq(t, app, "hanzo", "Q").StatusCode; code != 200 {
			t.Fatalf("Q req%d = %d, want 200 (project P's limit must not gate Q)", i+1, code)
		}
	}

	// Org isolation: org "other" has no rules — never throttled by hanzo's rule.
	for i := 0; i < 5; i++ {
		if code := rateReq(t, app, "other", "P").StatusCode; code != 200 {
			t.Fatalf("other req%d = %d, want 200 (org hanzo's limit must not gate org other)", i+1, code)
		}
	}
}
