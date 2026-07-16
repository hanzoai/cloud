package commerceinproc

import (
	"io"
	"net/http"
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

	SetApp(app)

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
