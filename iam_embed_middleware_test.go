package cloud

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/zap-proto/zip"
)

// TestIAMEmbedBehindMiddlewareChain locks the auth-critical contract for the
// embedded IAM surface (clients/iam): the /v1/iam/*, /.well-known/*, and
// /login/oauth/* paths pass through cloud's real request pipeline untouched for
// UNAUTHENTICATED callers.
//
// It wires the two middlewares that could wrongly reject an anonymous auth
// request — SanitizeIdentity (identity trust boundary) and BillingGate — in
// front of a stub IAM handler mounted the way iam mounts the real one, and
// asserts:
//
//   - unauth login/authorize/token/jwks reach the handler (2xx), never 402/503 —
//     hanzo.id's login + the operator SSO M2M mint (/v1/iam/oauth/token) must not
//     be balance-gated;
//   - a forged X-User-IsAdmin: true from the client is STRIPPED before the IAM
//     handler sees it (no admin escalation via a raw header);
//   - the gate is genuinely ENGAGED (a priced control path with a zero balance is
//     denied), so the IAM 2xx results prove the DefaultPrice==0 exemption rather
//     than a disabled gate.
//
// The metering client is Enabled() but backed by a zero-balance fakeCommerce, so
// any priced path it actually consults is denied.
func TestIAMEmbedBehindMiddlewareChain(t *testing.T) {
	fc := &fakeCommerce{balanceBody: `{"available":0}`}
	srv := fc.server(t)
	m := mustClient(t, srv.URL, false /* fail-closed */)

	app := zip.New(zip.Config{})
	// nil validator = the unauthenticated case: no JWKS, no principal established,
	// but forgeable X-User-*/X-Org-* headers are still stripped (defense in depth).
	app.Use(SanitizeIdentity(nil, "admin"))
	// DefaultPrice governs the IAM paths under test — that is the exemption being
	// asserted — but it prices NOTHING today (every subsystem self-meters in-handler,
	// and unpriced paths default to 0), so it cannot supply the control. The control
	// path carries its own price so the gate is proven LIVE; without that, the IAM
	// 2xx results below are equally explained by a gate that never denies anything.
	price := func(c *zip.Ctx) int64 {
		if c.Path() == controlPricedPath {
			return 1
		}
		return DefaultPrice(c)
	}
	app.Use(BillingGate(m, price))

	var sawForgedAdmin atomic.Bool
	stub := zip.AdaptNetHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-User-IsAdmin") != "" {
			sawForgedAdmin.Store(true)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	for _, p := range []string{"/v1/iam", "/.well-known", "/login/oauth"} {
		app.All(p+"/*", stub)
	}
	// Priced control path. With a zero balance the gate MUST deny it, proving the
	// gate is live. It is deliberately NOT an IAM path and not a real route: it
	// exists only to make "the gate denies when it should" observable.
	app.Post(controlPricedPath, func(c *zip.Ctx) error {
		return c.JSON(http.StatusOK, map[string]bool{"ok": true})
	})

	for _, tc := range []struct {
		method, path string
	}{
		{http.MethodPost, "/v1/iam/oauth/token"},
		{http.MethodPost, "/v1/iam/oauth/access_token"},
		{http.MethodPost, "/v1/iam/login"},
		{http.MethodGet, "/v1/iam/.well-known/jwks"},
		{http.MethodGet, "/v1/iam/.well-known/openid-configuration"},
		{http.MethodGet, "/.well-known/jwks"},
		{http.MethodGet, "/.well-known/openid-configuration"},
		{http.MethodGet, "/login/oauth/authorize"},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("X-User-IsAdmin", "true") // forgery attempt
		req.Header.Set("X-User-Id", "attacker")
		req.Header.Set("X-Org-Id", "attacker-org")
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			t.Errorf("%s %s = %d, want 2xx (unauth IAM endpoint must not be gated 402/503)", tc.method, tc.path, resp.StatusCode)
		}
	}

	if sawForgedAdmin.Load() {
		t.Error("client X-User-IsAdmin:true survived SanitizeIdentity to the IAM handler — admin forgery not stripped")
	}

	// Control: the gate must deny the priced path at zero balance. If this passes,
	// the gate is not engaged and the IAM 2xx results above prove nothing.
	req := httptest.NewRequest(http.MethodPost, controlPricedPath, nil)
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("control %s: %v", controlPricedPath, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		t.Errorf("priced %s returned 200 at zero balance — billing gate not engaged; the IAM 2xx assertions do not prove the exemption", controlPricedPath)
	}
}

// controlPricedPath is the synthetic priced route the gate-liveness control uses.
// It is not a product route: DefaultPrice charges nothing anywhere, so a control
// drawn from the real table cannot distinguish a live gate from a dead one.
const controlPricedPath = "/v1/probe/priced"

// TestDefaultPriceExemptsIAM pins the price-0 (ungated, unbilled) exemption for
// every IAM-owned prefix, so a future DefaultPrice edit that starts billing an
// auth path fails here rather than in production (the M2M token mint would 402).
func TestDefaultPriceExemptsIAM(t *testing.T) {
	for _, path := range []string{
		"/v1/iam/oauth/token",
		"/v1/iam/oauth/access_token",
		"/v1/iam/login",
		"/v1/iam/.well-known/jwks",
		"/.well-known/openid-configuration",
		"/.well-known/jwks",
		"/login/oauth/authorize",
	} {
		app := zip.New(zip.Config{})
		var got int64 = -1
		// DefaultPrice keys only on c.Path(), so one GET per path captures it.
		app.Get(path, func(c *zip.Ctx) error {
			got = DefaultPrice(c)
			return c.JSON(http.StatusOK, map[string]bool{"ok": true})
		})
		resp, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, path, nil))
		if err != nil {
			t.Fatalf("DefaultPrice(%s): %v", path, err)
		}
		_ = resp.Body.Close()
		if got != 0 {
			t.Errorf("DefaultPrice(%q) = %d, want 0 (IAM auth paths must never be billed)", path, got)
		}
	}
}
