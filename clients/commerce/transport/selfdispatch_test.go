package transport

import (
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// TestSetAppSelfDispatch pins the hazard that took /v1/billing/balance down in
// production, and that TestInProcessDispatch cannot see because it stubs a SEPARATE
// commerce handler via SetHandler — a condition that is FALSE in the shipped binary.
//
// SetApp publishes the HOST's shared zip app as the "commerce" transport, and RoundTrip
// re-dispatches BY PATH. commerce's own /v1/billing/* routes are NOT registered in the
// co-resident binary (api.Route() is called only from commerce's mount.go, which is
// //go:build cloud and never compiled). So when a cloud handler registered at path P
// "calls commerce" at that same P, the request re-enters THAT HANDLER — the proxy calls
// itself. The re-entrant request carries the service bearer but no validated principal,
// so the handler's own sign-in gate refuses it, and the outer hop reports the refusal as
// an upstream failure.
//
// This test asserts the CURRENT, REAL mechanics: the self-call happens, and it is
// BOUNDED AT DEPTH 2 by the sign-in gate (it is not infinite recursion).
func TestSetAppSelfDispatch(t *testing.T) {
	t.Cleanup(func() { SetHandler(nil) })

	var depth int32
	app := zip.New(zip.Config{})

	// A cloud-style handler at /v1/billing/balance that proxies to commerce at the SAME
	// path — exactly clients/billing.balance → proxy(s, c, "/v1/billing/balance").
	app.Get("/v1/billing/balance", func(c *zip.Ctx) error {
		d := atomic.AddInt32(&depth, 1)
		// The sign-in gate: no validated principal ⇒ refuse. On the re-entrant hop
		// there is none, which is what terminates the recursion.
		if d > 1 {
			return c.Bytes(http.StatusUnauthorized, []byte(`{"error":"sign in to view billing"}`))
		}
		req, err := http.NewRequest(http.MethodGet, BaseURL("")+"/v1/billing/balance", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer service-token")
		req.Header.Set("X-Org-Id", "hanzo")
		resp, err := Client(5 * time.Second).Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		// financeGet's shape: a non-2xx upstream becomes "billing upstream status %d".
		if resp.StatusCode != http.StatusOK {
			return c.Bytes(http.StatusBadGateway, []byte(`{"error":"billing upstream status `+http.StatusText(resp.StatusCode)+`"}`))
		}
		return c.Bytes(http.StatusOK, body)
	})

	SetApp(app.Fiber())

	req, err := http.NewRequest(http.MethodGet, BaseURL("")+"/v1/billing/balance", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := Client(5 * time.Second).Do(req)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	got := atomic.LoadInt32(&depth)
	t.Logf("handler entered %d times; outer status=%d body=%s", got, resp.StatusCode, body)

	if got != 2 {
		t.Fatalf("handler entered %d times, want exactly 2 (self-call, then the sign-in gate terminates it)", got)
	}
	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("outer status = %d, want 502 — the self-call is reported as an upstream failure", resp.StatusCode)
	}
}

// TestSpendAlertsAuthorizeShadowsBridge pins the fix for the /v1/billing/spend-alerts/authorize
// 502 self-dispatch loop the live cloud pod logged (~135×/30m, org "maxpower"): the request-edge
// metering gate (clients/metering scopeAuthorize) issues a SERVICE-token S2S GET of that path
// through THIS transport; with no co-resident handler it fell through to the account bridge's
// self-proxying /v1/billing/* wildcard (clients/account billingData → commerceDo, which re-dials
// COMMERCE_URL — the public edge = this binary — BY PATH through this same transport), re-entering
// the wildcard until the depth-8 guard refused → 502 → the cap gate fails OPEN.
//
// The fix (apps/commerce.go) registers commerce's own AuthorizeSpendCap co-resident, at the
// commerce mount (order 100) which is AHEAD of the account bridge (order 122), so the specific
// route shadows the wildcard and the gate's dispatch hits the real handler at depth 1 — no loop.
// This test models both arrangements on the transport this seam owns and pins the invariant.
func TestSpendAlertsAuthorizeShadowsBridge(t *testing.T) {
	const authorizePath = "/v1/billing/spend-alerts/authorize"

	// build assembles the co-resident app: commerce's specific authorize route (registered
	// FIRST, like mountCommerce at 100) — present only when withSpecific — then the account
	// bridge's self-proxying /v1/billing/* wildcard (registered LAST, like MountBridge at 122).
	// The wildcard re-forwards the SAME path back through the transport, exactly as the real
	// bridge does (host ignored, dispatched by path) — the move that self-loops.
	build := func(withSpecific bool) (app *zip.App, specificHits, wildcardHits *int32) {
		var sHits, wHits int32
		app = zip.New(zip.Config{})
		if withSpecific {
			app.Get(authorizePath, func(c *zip.Ctx) error {
				atomic.AddInt32(&sHits, 1)
				return c.Bytes(http.StatusOK, []byte(`{"allow":true}`)) // AuthorizeSpendCap's verdict shape.
			})
		}
		app.Get("/v1/billing/*", func(c *zip.Ctx) error {
			atomic.AddInt32(&wHits, 1)
			req, _ := http.NewRequest(http.MethodGet, BaseURL("")+authorizePath, nil)
			req.Header.Set("Authorization", "Bearer service-token")
			req.Header.Set("X-Org-Id", "maxpower")
			resp, err := Client(5 * time.Second).Do(req)
			if err != nil { // the depth guard's refusal surfaces here — the 502 the pod logged.
				return c.Bytes(http.StatusBadGateway, []byte(err.Error()))
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			return c.Bytes(resp.StatusCode, body)
		})
		return app, &sHits, &wHits
	}

	do := func(t *testing.T) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, BaseURL("")+authorizePath, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer service-token")
		req.Header.Set("X-Org-Id", "maxpower")
		resp, err := Client(5 * time.Second).Do(req)
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	// BUG (no co-resident handler): the wildcard self-loops until the depth guard refuses, so
	// it is entered exactly maxDepth times and the outer request surfaces the depth-exceeded 502.
	t.Run("bug_wildcard_self_loops", func(t *testing.T) {
		t.Cleanup(func() { SetHandler(nil) })
		app, specificHits, wildcardHits := build(false)
		SetApp(app.Fiber())
		status, body := do(t)
		t.Logf("no-shadow => status=%d wildcardHits=%d body=%s", status, atomic.LoadInt32(wildcardHits), body)
		if got := atomic.LoadInt32(specificHits); got != 0 {
			t.Fatalf("specific hits = %d, want 0 (no specific route registered)", got)
		}
		if got := atomic.LoadInt32(wildcardHits); got != maxDepth {
			t.Fatalf("wildcard entered %d times, want maxDepth=%d (the self-dispatch loop)", got, maxDepth)
		}
		if status != http.StatusBadGateway || !strings.Contains(body, "dispatch depth") {
			t.Fatalf("outer status=%d body=%q, want 502 carrying the depth-exceeded refusal", status, body)
		}
	})

	// FIX (co-resident specific route registered ahead of the wildcard): it shadows the
	// wildcard, so the gate's dispatch hits AuthorizeSpendCap once at depth 1, the wildcard's
	// self-proxy NEVER fires, and the verdict returns 200 — no loop, no 502.
	t.Run("fix_specific_route_shadows_wildcard", func(t *testing.T) {
		t.Cleanup(func() { SetHandler(nil) })
		app, specificHits, wildcardHits := build(true)
		SetApp(app.Fiber())
		status, body := do(t)
		t.Logf("shadowed => status=%d specificHits=%d wildcardHits=%d body=%s",
			status, atomic.LoadInt32(specificHits), atomic.LoadInt32(wildcardHits), body)
		if got := atomic.LoadInt32(specificHits); got != 1 {
			t.Fatalf("specific route entered %d times, want exactly 1 (served in-process at depth 1)", got)
		}
		if got := atomic.LoadInt32(wildcardHits); got != 0 {
			t.Fatalf("wildcard self-dispatch fired %d times, want 0 — the specific route must shadow it", got)
		}
		if status != http.StatusOK {
			t.Fatalf("outer status = %d, want 200 (the co-resident verdict, no self-dispatch 502)", status)
		}
	})
}

// assertPostShadowsBridge is the shared body of every "co-resident POST billing WRITE shadows
// the self-proxying /v1/billing/* bridge wildcard" regression — the whole class the P0 that
// broke console Billing → Credits belonged to. specificPattern is the route as registered
// co-resident (may carry a :param); requestPath is the concrete path the browser POSTs.
//
// It models both arrangements on the POST transport this seam owns:
//   - build(false) registers ONLY the account bridge's self-proxying POST /v1/billing/*
//     wildcard (like MountBridge at order 122): billingData → commerceDo re-dials COMMERCE_URL
//     (the public edge = this binary) BY PATH through THIS transport, re-entering the wildcard
//     until the depth-8 guard refuses — the exact user-facing 502.
//   - build(true) ALSO registers the specific commerce route FIRST (like mountCommerce at
//     order 100), so it shadows the wildcard and the write runs once at depth 1 — no loop.
func assertPostShadowsBridge(t *testing.T, specificPattern, requestPath string) {
	t.Helper()
	const okBody = `{"status":"ok"}`
	newReq := func() *http.Request {
		req, _ := http.NewRequest(http.MethodPost, BaseURL("")+requestPath, strings.NewReader(`{"sourceId":"cnon:card-nonce-ok"}`))
		req.Header.Set("Authorization", "Bearer service-token")
		req.Header.Set("X-Org-Id", "hanzo")
		return req
	}
	build := func(withSpecific bool) (app *zip.App, specificHits, wildcardHits *int32) {
		var sHits, wHits int32
		app = zip.New(zip.Config{})
		if withSpecific {
			app.Post(specificPattern, func(c *zip.Ctx) error {
				atomic.AddInt32(&sHits, 1)
				return c.Bytes(http.StatusOK, []byte(okBody))
			})
		}
		app.Post("/v1/billing/*", func(c *zip.Ctx) error {
			atomic.AddInt32(&wHits, 1)
			resp, err := Client(5 * time.Second).Do(newReq())
			if err != nil { // the depth guard's refusal surfaces here — the 502 the user saw.
				return c.Bytes(http.StatusBadGateway, []byte(err.Error()))
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			return c.Bytes(resp.StatusCode, body)
		})
		return app, &sHits, &wHits
	}
	do := func() (int, string) {
		resp, err := Client(5 * time.Second).Do(newReq())
		if err != nil {
			t.Fatalf("dispatch: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	// BUG (no co-resident handler): the wildcard self-loops until the depth guard refuses, so
	// it is entered exactly maxDepth times and the outer request surfaces the depth-exceeded
	// 502 carrying the EXACT re-entrancy refusal for THIS route.
	t.Run("bug_wildcard_self_loops", func(t *testing.T) {
		t.Cleanup(func() { SetHandler(nil) })
		app, specificHits, wildcardHits := build(false)
		SetApp(app.Fiber())
		status, body := do()
		t.Logf("no-shadow %s => status=%d wildcardHits=%d body=%s", requestPath, status, atomic.LoadInt32(wildcardHits), body)
		if got := atomic.LoadInt32(specificHits); got != 0 {
			t.Fatalf("specific hits = %d, want 0 (no specific route registered)", got)
		}
		if got := atomic.LoadInt32(wildcardHits); got != maxDepth {
			t.Fatalf("wildcard entered %d times, want maxDepth=%d (the self-dispatch loop)", got, maxDepth)
		}
		if status != http.StatusBadGateway ||
			!strings.Contains(body, "dispatch depth") ||
			!strings.Contains(body, "POST "+requestPath) {
			t.Fatalf("outer status=%d body=%q, want 502 carrying the depth-exceeded refusal for POST %s", status, body, requestPath)
		}
	})

	// FIX (co-resident specific route registered ahead of the wildcard): it shadows the
	// wildcard, so the handler runs once at depth 1, the wildcard's self-proxy NEVER fires,
	// and the write returns 200 — no loop, no depth-8 502.
	t.Run("fix_specific_route_shadows_wildcard", func(t *testing.T) {
		t.Cleanup(func() { SetHandler(nil) })
		app, specificHits, wildcardHits := build(true)
		SetApp(app.Fiber())
		status, body := do()
		t.Logf("shadowed %s => status=%d specificHits=%d wildcardHits=%d body=%s",
			requestPath, status, atomic.LoadInt32(specificHits), atomic.LoadInt32(wildcardHits), body)
		if got := atomic.LoadInt32(specificHits); got != 1 {
			t.Fatalf("specific route entered %d times, want exactly 1 (served in-process at depth 1)", got)
		}
		if got := atomic.LoadInt32(wildcardHits); got != 0 {
			t.Fatalf("wildcard self-dispatch fired %d times, want 0 — the specific route must shadow it", got)
		}
		if status != http.StatusOK {
			t.Fatalf("outer status = %d, want 200 (the co-resident write, no self-dispatch 502)", status)
		}
	})
}

// TestTopupTokenShadowsBridge — the reported P0: POST /v1/billing/topup/token (inline Square
// card top-up) self-dispatched to the depth-8 502 until commerce's TopupWithToken was
// registered co-resident (apps/commerce.go mountCommerce).
func TestTopupTokenShadowsBridge(t *testing.T) {
	assertPostShadowsBridge(t, "/v1/billing/topup/token", "/v1/billing/topup/token")
}

// TestPaymentMethodsShadowsBridge — the save-card write (POST /v1/billing/payment-methods →
// commerce CreatePaymentMethod), same self-dispatch class, now shadowed co-resident.
func TestPaymentMethodsShadowsBridge(t *testing.T) {
	assertPostShadowsBridge(t, "/v1/billing/payment-methods", "/v1/billing/payment-methods")
}

// TestSubscriptionCancelShadowsBridge — POST /v1/billing/subscriptions/:id/cancel →
// commerce CancelBillingSubscription, same class, now shadowed co-resident.
func TestSubscriptionCancelShadowsBridge(t *testing.T) {
	assertPostShadowsBridge(t, "/v1/billing/subscriptions/:id/cancel", "/v1/billing/subscriptions/sub_1/cancel")
}

// TestSubscriptionReactivateShadowsBridge — POST /v1/billing/subscriptions/:id/reactivate →
// commerce ReactivateBillingSubscription, same class, now shadowed co-resident.
func TestSubscriptionReactivateShadowsBridge(t *testing.T) {
	assertPostShadowsBridge(t, "/v1/billing/subscriptions/:id/reactivate", "/v1/billing/subscriptions/sub_1/reactivate")
}
