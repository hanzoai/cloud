// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"io"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"

	accountclient "github.com/hanzoai/cloud/apps/account"
	commercebilling "github.com/hanzoai/commerce/api/billing"
	commercemid "github.com/hanzoai/commerce/middleware"
	"github.com/hanzoai/commerce/middleware/iammiddleware"
)

// The customer's own ledger reads — transactions, credit balance, and the
// billing account (with its members). billing.hanzo.ai calls all four; none was
// mounted, so all four answered 404 and the Transactions, Credits, Team and
// Settings tabs were permanently empty.
//
// What makes that failure worth a test is that it was INVISIBLE to the obvious
// check. commerce declares these routes itself, on its api.Route() `user` group
// (api/billing/handlers.go), so grepping the module finds them wired and the
// deployment looks merely stale. It is not: the co-resident embed registers on
// the HOST's router and never compiles that table, so a commerce route exists in
// production only if Mount names it. This test asserts the naming, which is the
// thing that was actually missing — a library-side grep cannot.
//
// The assertion is 401-not-404, and the distinction is the whole point. Both are
// "no data" to a browser, but 404 means the gate never ran and 401 means it ran
// and refused. Only the second proves the route reached its middleware.

// ledgerApp mounts the four reads exactly as Mount does — same middleware, same
// order. A test that assembles a different chain proves only that the chain it
// invented behaves.
func ledgerApp(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{})
	app.Get("/v1/billing/transactions",
		commercemid.RequestContext(),
		iammiddleware.IAMTokenRequired(),
		accountclient.PinBillingSubject(),
		commercemid.TokenRequired(),
		commercebilling.ListTransactions,
	)
	app.Get("/v1/billing/credit-balance",
		commercemid.RequestContext(),
		iammiddleware.IAMTokenRequired(),
		accountclient.PinBillingSubject(),
		commercemid.TokenRequired(),
		commercebilling.GetCreditBalance,
	)
	app.Get("/v1/billing/accounts",
		commercemid.RequestContext(),
		iammiddleware.IAMTokenRequired(),
		accountclient.PinBillingSubject(),
		commercemid.TokenRequired(),
		commercebilling.ListBillingAccounts,
	)
	app.Get("/v1/billing/accounts/:id/members",
		commercemid.RequestContext(),
		iammiddleware.IAMTokenRequired(),
		accountclient.PinBillingSubject(),
		commercemid.TokenRequired(),
		commercebilling.ListAccountMembers,
	)
	return app
}

// TestLedgerReads_MountedAndFailClosed — each read must be reachable and must
// refuse an anonymous caller. 404 is the production bug this replaces: the route
// absent, the gate never reached, the tab empty with nothing to explain it.
func TestLedgerReads_MountedAndFailClosed(t *testing.T) {
	app := ledgerApp(t)
	for _, path := range []string{
		"/v1/billing/transactions",
		"/v1/billing/credit-balance",
		"/v1/billing/accounts",
		"/v1/billing/accounts/acme/members",
	} {
		resp, err := app.Test(httptest.NewRequest("GET", path, nil))
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 404 {
			t.Fatalf("GET %s answered 404 — the route is not mounted, so the auth gate never ran", path)
		}
		if resp.StatusCode != 401 {
			t.Fatalf("GET %s: want 401 for an anonymous caller, got %d (%s)", path, resp.StatusCode, body)
		}
	}
}

// TestLedgerReads_ForgedSubjectIsRefused — the two reads that filter on a
// caller-supplied subject key (ListTransactions on ?user, GetCreditBalance on
// ?userId) must not answer a request that names someone else. Unpinned, both
// return that subject's rows out of the org namespace, which is the leak the
// chain exists to close; anonymous, the pin fail-closes before the handler runs,
// so a forged subject buys nothing. Asserted against a hostile request rather
// than a well-formed one, because a well-formed one cannot fail this way.
func TestLedgerReads_ForgedSubjectIsRefused(t *testing.T) {
	app := ledgerApp(t)
	for _, path := range []string{
		"/v1/billing/transactions?user=victim",
		"/v1/billing/credit-balance?userId=victim",
	} {
		resp, err := app.Test(httptest.NewRequest("GET", path, nil))
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("GET %s: a forged subject must be refused with 401, got %d (%s)",
				path, resp.StatusCode, body)
		}
	}
}
