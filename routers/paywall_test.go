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
	// The middleware reads enforcement per request; the tests pin it to a constant.
	app.Use(Paywall(func() bool { return enforced }, plans))
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
		"/v1/auth/signin", "/v1/auth/signout", "/v1/auth/account",
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

// A gate whose bypass is a shift key is not a gate: before canonical(), gated()
// tested strings.HasPrefix(path, "/v1/") against the raw path, so any casing of
// the version segment skipped the paywall and reached the handler unpaid.
func TestGated_CaseFoldedSoCasingCannotBypass(t *testing.T) {
	for _, path := range []string{
		"/v1/chat/completions",
		"/V1/chat/completions",
		"/V1/CHAT/COMPLETIONS",
		"/v1/analytics/events",
		"/V1/analytics/events",
	} {
		if !gated(path) {
			t.Errorf("gated(%q) = false, want true — this path skips the paywall", path)
		}
	}
}

// The mirror of the same bug, in the direction that locks a paying user OUT: an
// allow-listed route in other casing missed the allow list and would have been
// refused 402 — on the OAuth callback, of all routes.
func TestGated_CaseFoldedSoAllowlistStillMatches(t *testing.T) {
	for _, path := range []string{
		"/v1/iam/callback",
		"/V1/iam/callback",
		"/v1/IAM/callback",
		"/V1/BILLING/subscribe",
		"/V1/auth/signin",
	} {
		if gated(path) {
			t.Errorf("gated(%q) = true, want false — 402 on the sell/service surface", path)
		}
	}
}

// A trailing slash means the same route. It used to match neither the exact-match
// list nor any allow prefix, so /v1/signin/ was paywalled: a 402 in front of sign-in.
func TestGated_TrailingSlashDoesNotLockOut(t *testing.T) {
	for _, path := range []string{
		"/v1/auth/signin/",
		"/v1/auth/signout/",
		"/v1/auth/account/",
		"/v1/entitlements/",
		"/v1/plans/",
		"/v1/models/",
		"/v1/health/",
		"/v1/billing/subscribe/",
	} {
		if gated(path) {
			t.Errorf("gated(%q) = true, want false — trailing slash must not gate a sell/service route", path)
		}
	}
}

// Folding must not open a hole: a gated product route stays gated with a trailing
// slash, and the bare version root is left exactly as the router had it.
func TestGated_TrailingSlashDoesNotOpenAHole(t *testing.T) {
	for _, path := range []string{
		"/v1/chat/completions/",
		"/V1/chat/completions/",
		"/v1/analytics/events/",
	} {
		if !gated(path) {
			t.Errorf("gated(%q) = false, want true — trailing slash must not bypass", path)
		}
	}
	if got := canonical("/v1/"); got != "/v1/" {
		t.Errorf("canonical(%q) = %q, want it untouched", "/v1/", got)
	}
	for _, path := range []string{"/healthz", "/readyz", "/zap", "/", "/assets/app.js"} {
		if gated(path) {
			t.Errorf("gated(%q) = true, want false — non-/v1 surface", path)
		}
	}
}

// Enforcement is read per request, so flipping the cockpit switch takes effect on the
// NEXT request against an already-mounted app. Before this, `enforced` was evaluated
// once at mount and the only way to change it was to restart the binary — which is why
// the switch registered in the cockpit governed nothing.
func TestPaywall_EnforcementIsReadPerRequest(t *testing.T) {
	var enforced atomic.Bool // starts false: dark
	var ran atomic.Bool
	app := zip.New(zip.Config{})
	app.Use(Paywall(enforced.Load, &fakePlans{paid: false}))
	app.Get("/*", func(c *zip.Ctx) error {
		ran.Store(true)
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})

	ran.Store(false)
	if resp := do(t, app, "/v1/chat/completions", validated); resp.StatusCode != http.StatusOK {
		t.Fatalf("dark: status = %d, want 200", resp.StatusCode)
	}
	if !ran.Load() {
		t.Fatal("dark: handler did not run")
	}

	enforced.Store(true) // the owner flips it in admin.hanzo.ai

	ran.Store(false)
	resp := do(t, app, "/v1/chat/completions", validated)
	if resp.StatusCode != http.StatusPaymentRequired {
		t.Fatalf("after flip: status = %d, want 402 — the switch did not hot-apply", resp.StatusCode)
	}
	if ran.Load() {
		t.Fatal("after flip: handler ran despite 402")
	}

	enforced.Store(false) // and flips it back — the kill switch must restore access

	ran.Store(false)
	if resp := do(t, app, "/v1/chat/completions", validated); resp.StatusCode != http.StatusOK {
		t.Fatalf("after revert: status = %d, want 200", resp.StatusCode)
	}
	if !ran.Load() {
		t.Fatal("after revert: handler did not run")
	}
}

// A nil reader is the dark default and must never gate.
func TestPaywall_NilReaderPassesEverything(t *testing.T) {
	var ran atomic.Bool
	app := zip.New(zip.Config{})
	app.Use(Paywall(nil, &fakePlans{paid: false}))
	app.Get("/*", func(c *zip.Ctx) error {
		ran.Store(true)
		return c.JSON(http.StatusOK, map[string]string{"ok": "true"})
	})
	if resp := do(t, app, "/v1/chat/completions", validated); resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !ran.Load() {
		t.Fatal("handler did not run")
	}
}

// TestAuthRoutesAreNeverPaywalled pins the one failure this gate must never have:
// a 402 in front of signing in.
//
// The allow-list matches by STRING, so it does NOT move when a route moves. When
// the /v1 surface was namespaced (/v1/signin → /v1/auth/signin, /v1/get-account →
// /v1/auth/account) the paths changed underneath this list, and a list left
// un-updated would have gated the entire sell/service entry path — the same shape
// of outage the trailing-slash and case-folding bugs already caused here twice.
//
// The names below are the paths the console and every client actually request. If
// a route moves again, this test fails rather than production.
func TestAuthRoutesAreNeverPaywalled(t *testing.T) {
	for _, p := range []string{
		"/v1/auth/signin",
		"/v1/auth/signout",
		"/v1/auth/account",
	} {
		if gated(p) {
			t.Errorf("gated(%q) = true — a 402 in front of sign-in", p)
		}
		// The same route must survive the two shapes canonical() folds, or the
		// gate disagrees with itself for a caller that sent a trailing slash or
		// different case.
		for _, variant := range []string{p + "/", strings.ToUpper(p)} {
			if gated(variant) {
				t.Errorf("gated(%q) = true — the folded form must be exempt too", variant)
			}
		}
	}

	// And the old spellings must NOT be silently exempt: they name routes that no
	// longer exist, so leaving them on the list would be dead weight that reads
	// like coverage.
	for _, dead := range []string{"/v1/signin", "/v1/signout", "/v1/get-account"} {
		if !gated(dead) {
			t.Errorf("%q is still allow-listed — it names a route that no longer exists", dead)
		}
	}
}
