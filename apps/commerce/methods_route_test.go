// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"

	accountclient "github.com/hanzoai/cloud/apps/account"
	commercebilling "github.com/hanzoai/commerce/api/billing"
	commercemid "github.com/hanzoai/commerce/middleware"
	"github.com/hanzoai/commerce/middleware/iammiddleware"
)

// The saved-card address, /v1/billing/methods, moved from cloud's billing app —
// where it was FORWARDED to commerce over HTTP through a base URL this
// deployment never sets — to here, where commerce is already in the process.
// The symptom of the hop was a customer getting 401 while listing their own
// cards, and a checkout that could not prefill.
//
// Its tests had to move with it, and they were the reason to write these: the
// billing app's versions asserted the PROXY (which commerce path was called,
// which query the forwarder pinned). None of that exists anymore, and deleting
// them without replacement would have taken the two facts that still matter
// with it. Both survive the move because they are properties of the route, not
// of the transport:
//
//	a caller must be authenticated — saving a card is a customer's own act
//	the subject is PINNED server-side — never read from what the caller sent
//
// The second is what keeps one customer out of another's cards, so it is
// asserted against a hostile request rather than a well-formed one.

// methodsApp mounts the customer saved-card routes exactly as Mount does — same
// middleware, same order. A test that assembles a different chain proves only
// that the chain it invented behaves.
func methodsApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{})
	app.Get("/v1/billing/methods",
		commercemid.RequestContext(),
		iammiddleware.IAMTokenRequired(),
		accountclient.PinBillingSubject(),
		commercebilling.ListPaymentMethods,
	)
	app.Post("/v1/billing/methods",
		commercemid.RequestContext(),
		iammiddleware.IAMTokenRequired(),
		accountclient.PinBillingSubject(),
		commercebilling.CreatePaymentMethod,
	)
	return app
}

// TestMethods_Unauthenticated401 — an anonymous caller must not reach the
// handler at all. The failure this guards is not a leak but a 404: when the
// route is not mounted, an unauthenticated request gets "no such address",
// which reads like a missing feature and hides that the gate never ran.
func TestMethods_Unauthenticated401(t *testing.T) {
	app := methodsApp(t)
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/v1/billing/methods", ""},
		{"POST", "/v1/billing/methods", `{"sourceId":"cnon:fake"}`},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		if tc.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 404 {
			t.Fatalf("%s %s answered 404 — the route is not mounted, so the auth gate never ran", tc.method, tc.path)
		}
		if resp.StatusCode != 401 {
			t.Fatalf("%s %s: want 401 for an anonymous caller, got %d (%s)", tc.method, tc.path, resp.StatusCode, body)
		}
	}
}
