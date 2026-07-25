package routers

// Unit tests for the subscription paywall, driven end-to-end through the real zip
// stack against a fake PlanChecker whose verdict each test controls. Principal state is
// set exactly as the identity boundary would leave it: X-User-Id = validated principal,
// X-Org-Id = the org, X-User-IsAdmin = platform super-admin.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/zap-proto/zip"
)

// fakePlans is a controllable PlanChecker. calls counts how often the paywall actually
// consulted commerce, so a test can prove an allow-listed / admin / dark-ship request
// short-circuits BEFORE the plan read.
type fakePlans struct {
	tier  string
	paid  bool
	err   error
	calls atomic.Int32
}

func (f *fakePlans) ActivePaidPlan(_ context.Context, _ string) (string, bool, error) {
	f.calls.Add(1)
	return f.tier, f.paid, f.err
}

// newApp wires Paywall(enforced, plans) ahead of a catch-all that records whether the
// handler ran and answers 200 — so a 402/deny is observable as "handler did not run".
func newApp(enforced bool, plans PlanChecker, ran *atomic.Bool) *zip.App {
	app := zip.New(zip.Config{})
	app.Use(Paywall(enforced, plans))
	app.Get("/*", func(c *zip.Ctx) error {
		ran.Store(true)
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})
	return app
}

// principal header sets, exactly as SanitizeIdentity leaves them.
var (
	validated = map[string]string{"X-User-Id": "alice", "X-Org-Id": "acme"}
	superUser = map[string]string{"X-User-Id": "z", "X-Org-Id": "admin", "X-User-IsAdmin": "true"}
	anonymous = map[string]string{} // no X-User-Id → not a validated principal.
)

func do(t *testing.T, app *zip.App, path string, headers map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("Test %s: %v", path, err)
	}
	return resp
}

func bodyOf(t *testing.T, resp *http.Response) string {
	t.Helper()
	b, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return string(b)
}

// (1) A validated org WITH an active paid plan reaches a gated product route → 200.
func TestPaywall_ActivePlanPasses(t *testing.T) {
	f := &fakePlans{tier: "pro", paid: true}
	var ran atomic.Bool
	app := newApp(true, f, &ran)

	resp := do(t, app, "/v1/agents/list", validated)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !ran.Load() {
		t.Fatal("handler did not run for a paid org")
	}
	if f.calls.Load() != 1 {
		t.Fatalf("ActivePaidPlan calls = %d, want 1", f.calls.Load())
	}
}

// (2) A validated, non-admin org with NO paid plan on a gated route → 402
// subscription_required, exact body shape, handler NOT run.
func TestPaywall_NoPlan402(t *testing.T) {
	f := &fakePlans{paid: false}
	var ran atomic.Bool
	app := newApp(true, f, &ran)

	resp := do(t, app, "/v1/agents/list", validated)
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("status = %d, want 402", resp.StatusCode)
	}
	if ran.Load() {
		t.Fatal("handler ran despite the 402")
	}
	body := bodyOf(t, resp)
	for _, want := range []string{
		`"error":"subscription_required"`,
		`"plan":"pro"`,
		`"price":"$20/mo"`,
		`"url":"https://cloud.hanzo.ai/plans"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("402 body %q missing %q", body, want)
		}
	}
}

// (3) Every allow-listed sell/service route passes WITH no plan — and never even
// consults commerce (short-circuits before the plan read), so a paywall in front of the
// pay button can never happen.
func TestPaywall_AllowlistedPassWithNoPlan(t *testing.T) {
	paths := []string{
		"/v1/signin", "/v1/signout", "/v1/get-account",
		"/v1/billing/plans", "/v1/billing/subscriptions", "/v1/billing/balance",
		"/v1/billing/payment-methods", "/v1/billing/usage",
		"/v1/plans", "/v1/plans/resolve/pro",
		"/v1/models", "/v1/models/zen-1",
		"/v1/iam/login", "/v1/iam/oauth/token",
		"/v1/entitlements",
		"/v1/health", "/v1/ai/health", "/v1/kms/health",
	}
	for _, p := range paths {
		f := &fakePlans{paid: false}
		var ran atomic.Bool
		app := newApp(true, f, &ran)

		resp := do(t, app, p, validated)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 (allow-listed)", p, resp.StatusCode)
		}
		if !ran.Load() {
			t.Errorf("%s: handler did not run (should be exempt)", p)
		}
		if f.calls.Load() != 0 {
			t.Errorf("%s: consulted commerce %d times, want 0 (short-circuit)", p, f.calls.Load())
		}
	}
}

// (4) A platform super-admin bypasses the gate even with no paid plan.
func TestPaywall_AdminBypasses(t *testing.T) {
	f := &fakePlans{paid: false}
	var ran atomic.Bool
	app := newApp(true, f, &ran)

	resp := do(t, app, "/v1/agents/list", superUser)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (admin bypass)", resp.StatusCode)
	}
	if !ran.Load() {
		t.Fatal("handler did not run for a super-admin")
	}
	if f.calls.Load() != 0 {
		t.Fatalf("admin consulted commerce %d times, want 0 (bypass before the read)", f.calls.Load())
	}
}

// (5) PAYWALL_ENFORCED=false → dark ship: EVERYTHING passes, and commerce is never
// consulted, so there is zero behavior change until an owner flips the flag.
func TestPaywall_DisabledPassesEverything(t *testing.T) {
	f := &fakePlans{paid: false}
	var ran atomic.Bool
	app := newApp(false, f, &ran)

	resp := do(t, app, "/v1/agents/list", validated)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (dark ship)", resp.StatusCode)
	}
	if !ran.Load() {
		t.Fatal("handler did not run while paywall disabled")
	}
	if f.calls.Load() != 0 {
		t.Fatalf("disabled paywall consulted commerce %d times, want 0", f.calls.Load())
	}
}

// (6) A commerce/plan machinery error fails OPEN — an outage never locks out a
// subscriber.
func TestPaywall_FailOpenOnError(t *testing.T) {
	f := &fakePlans{err: io.ErrUnexpectedEOF}
	var ran atomic.Bool
	app := newApp(true, f, &ran)

	resp := do(t, app, "/v1/agents/list", validated)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (fail open on error)", resp.StatusCode)
	}
	if !ran.Load() {
		t.Fatal("handler did not run on a plan-read error (should fail open)")
	}
}

// (7) A nil PlanChecker (commerce cannot answer — split-deploy / disabled stub) fails
// OPEN.
func TestPaywall_NilCheckerFailOpen(t *testing.T) {
	var ran atomic.Bool
	app := newApp(true, nil, &ran)

	resp := do(t, app, "/v1/agents/list", validated)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (nil checker → fail open)", resp.StatusCode)
	}
	if !ran.Load() {
		t.Fatal("handler did not run with a nil PlanChecker")
	}
}

// (8) An anonymous request (no validated principal) is admitted — the route's own auth
// answers it; a 402 upgrade prompt is meaningless to someone not signed in, and the org
// is untrusted anyway. Commerce is never consulted on an unvalidated org.
func TestPaywall_AnonymousPasses(t *testing.T) {
	f := &fakePlans{paid: false}
	var ran atomic.Bool
	app := newApp(true, f, &ran)

	resp := do(t, app, "/v1/agents/list", anonymous)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (anonymous → route auth answers)", resp.StatusCode)
	}
	if f.calls.Load() != 0 {
		t.Fatalf("anonymous consulted commerce %d times, want 0", f.calls.Load())
	}
}

// (9) A non-/v1 path (the SPA shell / static asset) is never gated, so the app that
// renders the upgrade prompt always loads — even for a planless org.
func TestPaywall_NonV1PathPasses(t *testing.T) {
	f := &fakePlans{paid: false}
	var ran atomic.Bool
	app := newApp(true, f, &ran)

	for _, p := range []string{"/", "/billing", "/plans", "/assets/app.js"} {
		f.calls.Store(0)
		ran.Store(false)
		resp := do(t, app, p, validated)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 (non-/v1 never gated)", p, resp.StatusCode)
		}
		if f.calls.Load() != 0 {
			t.Errorf("%s: consulted commerce %d times, want 0", p, f.calls.Load())
		}
	}
}
