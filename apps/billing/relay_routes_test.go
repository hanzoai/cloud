package billing

import (
	"net/http"
	"testing"
)

// Every relayed address must be MOUNTED, and must FAIL CLOSED.
//
// This carries a guarantee that used to live elsewhere, on the app that used to
// serve these addresses. Its lesson survived the fold and is worth restating
// where the endpoints are now: a route reaches an app only when both halves of
// its address exist, and the router's half is a different file from the app's.
// The manifest oracle asserts the router's half; this asserts the app's, and
// neither can see the other.
//
// The assertion is NOT-404, and that distinction is the whole point. Both 404
// and 401 are "no data" to a browser, but 404 means the route was never
// registered and the gate never ran, while 401 or 403 means it ran and refused.
// Only the second proves the address exists. The defect it guards is the one
// that survived a correct fix once: the route strings were in the shipped binary
// the entire time production answered 404 for them, because nothing mounted them
// on the router that took the request.
//
// It is deliberately about the endpoints this app took over in the fold, listed
// leaf by leaf rather than by prefix — a prefix is a claim about a subtree, and
// a mount that missed one leaf inside a claimed subtree is exactly what this
// catches.
func TestRelayedEndpointsAreMountedAndFailClosed(t *testing.T) {
	f := &fakeCommerce{status: 200, body: `{}`}
	app := mountApp(t, f.server(t).URL)

	for _, r := range []struct{ method, path string }{
		{http.MethodGet, "/v1/billing/accounts"},
		{http.MethodGet, "/v1/billing/accounts/acme/members"},
		{http.MethodGet, "/v1/billing/transactions"},
		{http.MethodGet, "/v1/billing/payouts"},
		{http.MethodGet, "/v1/billing/credits"},
		{http.MethodGet, "/v1/billing/credit-balance"},
		{http.MethodGet, "/v1/billing/credit-balance/breakdown"},
		{http.MethodGet, "/v1/billing/invoices"},
		{http.MethodGet, "/v1/billing/invoices/inv_1"},
		{http.MethodGet, "/v1/billing/invoices/inv_1/pdf"},
		{http.MethodPost, "/v1/billing/invoices"},
		{http.MethodPost, "/v1/billing/invoices/inv_1/issue"},
		{http.MethodPost, "/v1/billing/invoices/inv_1/collect"},
		{http.MethodPost, "/v1/billing/invoices/inv_1/void"},
		{http.MethodGet, "/v1/billing/alerts"},
		{http.MethodPost, "/v1/billing/alerts"},
		{http.MethodPatch, "/v1/billing/alerts/a_1"},
		{http.MethodDelete, "/v1/billing/alerts/a_1"},
		{http.MethodGet, "/v1/billing/methods"},
		{http.MethodPost, "/v1/billing/methods"},
		{http.MethodDelete, "/v1/billing/methods/pm_1"},
		{http.MethodGet, "/v1/billing/portal/methods"},
		{http.MethodPost, "/v1/billing/portal/methods"},
		{http.MethodDelete, "/v1/billing/portal/methods/pm_1"},
		{http.MethodGet, "/v1/billing/settings"},
		{http.MethodPost, "/v1/billing/mode"},
		{http.MethodGet, "/v1/billing/tier"},
		{http.MethodGet, "/v1/billing/usage/rollup"},
		{http.MethodGet, "/v1/billing/subscriptions"},
		{http.MethodPost, "/v1/billing/subscriptions/s_1/cancel"},
		{http.MethodPost, "/v1/billing/subscriptions/s_1/reactivate"},
		{http.MethodPost, "/v1/billing/topup"},
		{http.MethodPost, "/v1/billing/topup/token"},
		{http.MethodPost, "/v1/billing/subscribe/card"},
		{http.MethodPost, "/v1/billing/recharge/run-all"},
		{http.MethodGet, "/v1/billing/wire"},
		{http.MethodGet, "/v1/billing/crypto/options"},
		{http.MethodPost, "/v1/billing/crypto/deposit"},
		{http.MethodGet, "/v1/billing/crypto/deposit/d_1"},
	} {
		code, body := call(t, app, r.method, r.path, "", "")
		if code == http.StatusNotFound {
			t.Errorf("%s %s = 404 — the address is not mounted, so no gate ran and no "+
				"caller can reach it however well the handler behaves (%s)",
				r.method, r.path, body)
			continue
		}
		if code < 400 {
			t.Errorf("%s %s = %d for a caller with no validated principal — this endpoint "+
				"answers money questions and must refuse one (%s)",
				r.method, r.path, code, body)
		}
	}
}

// The public catalog is the ONE endpoint here that must answer an anonymous
// caller, and it is asserted separately for exactly that reason: folding it
// into the list above would let a refusal there pass as correct, and a plans
// page that requires a session is a paywall in front of the prices.
func TestTheCatalogAnswersAnonymously(t *testing.T) {
	f := &fakeCommerce{status: 200, body: `{}`}
	app := mountApp(t, f.server(t).URL)

	code, body := call(t, app, http.MethodGet, "/v1/billing/plans", "", "")
	if code == http.StatusNotFound {
		t.Fatalf("/v1/billing/plans = 404 — the catalog is not mounted (%s)", body)
	}
	if code == http.StatusUnauthorized || code == http.StatusForbidden {
		t.Fatalf("/v1/billing/plans = %d for an anonymous caller — the catalog is what "+
			"anyone may buy, and a session in front of the prices is a paywall (%s)", code, body)
	}
}
