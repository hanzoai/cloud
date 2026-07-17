package account

import (
	"net/http"
	"testing"
)

// bridge_mint_test.go — the privilege-escalation boundary of the /v1/billing/*
// bridge: an ordinary signed-in ORG user must never reach commerce's money-MINT
// surface.
//
// THE ESCALATION THIS LOCKS OUT. The bridge forwards to commerce with the admin
// COMMERCE_SERVICE_TOKEN. Commerce gates every mint on
// MayMintMoney(c) = IsServiceToken(c) || IsSuperAdmin(c) (middleware/platformonly.go)
// — and the bridge's service token satisfies IsServiceToken. So ANY subpath the
// bridge forwards is executed by commerce as the PLATFORM, not as the caller.
// billingData scopes the SUBJECT to the caller's own account, which is exactly the
// attack rather than a defense: an org user mints to THEMSELVES. Commerce's own
// gate comment names this: "let ANY org owner self-credit unlimited balance (POST
// /v1/billing/deposit &c.) → unlimited free inference."
//
// Commerce 403s that same org admin when they call it DIRECTLY
// (TestC1_OrgAdminDeniedOnEveryMintRoute) and mints 201 for the service token
// (TestC1_ServiceTokenMintsDeposit). The bridge is what converts the former into
// the latter. The gate therefore has to live HERE, at the point that hands out the
// token: forwardable subpaths are an ALLOWLIST, and a mint path is not on it.
//
// alice is an ordinary org user — X-Org-Id "acme", owner != "admin", NOT a
// SuperAdmin — i.e. precisely the principal commerce refuses at the front door.

// TestBridge_OrgUserCannotReachMint is the reproduction. Each of these commerce
// subpaths is PlatformOnly-gated (api/billing/handlers.go: `mintRequired`), meaning
// possession of the service token IS authority to create spendable balance. None
// may leave cloud. A request that never reaches commerce cannot mint, so the
// assertion is twofold: the caller is refused AND upstream saw nothing.
func TestBridge_OrgUserCannotReachMint(t *testing.T) {
	// Every money-MINT route commerce gates on PlatformOnly. Kept in lockstep with
	// api/billing/handlers.go's `mintRequired` routes.
	mintPaths := []struct{ method, path, body string }{
		{http.MethodPost, "/v1/billing/deposit", `{"amount":100000000,"currency":"usd"}`},
		{http.MethodPost, "/v1/billing/credit", `{"amountCents":100000000}`},
		{http.MethodPost, "/v1/billing/refund", `{"amount":100000000}`},
		{http.MethodPost, "/v1/billing/credit-grants", `{"amount":100000000}`},
		{http.MethodPost, "/v1/billing/credit-grants/g1/void", ``},
		{http.MethodPost, "/v1/billing/allotment/grant", `{"plan":"enterprise"}`},
		{http.MethodPost, "/v1/billing/allotment/run", ``},
		{http.MethodPost, "/v1/billing/husd/sync", ``},
		{http.MethodPost, "/v1/billing/husd/settle", ``},
		{http.MethodPost, "/v1/billing/husd/migrate", ``},
	}

	for _, m := range mintPaths {
		t.Run(m.path, func(t *testing.T) {
			f := &fakeBilling{}
			t.Setenv("COMMERCE_URL", f.server(t).URL)
			t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-tok")
			app := mountApp(t, "http://iam.invalid", "", "")

			code, body := callH(t, app, m.method, m.path, alice, m.body)

			// The mint request must NEVER reach commerce: arriving there at all means
			// it arrived bearing the admin service token, which IS the authority to
			// mint (MayMintMoney → AuthorizeMint → the ledger write).
			if f.path != "" {
				t.Fatalf("ESCALATION: an ordinary org user's %s %s reached commerce at %q "+
					"carrying %q — the service token that satisfies MayMintMoney. "+
					"Minted subject=%v amount=%v in org=%q.",
					m.method, m.path, f.path, f.auth, f.body["user"], f.body["amount"], f.org)
			}
			if code != http.StatusNotFound {
				t.Fatalf("%s %s: want 404 (not a forwardable billing endpoint), got %d (%s)",
					m.method, m.path, code, body)
			}
		})
	}
}

// TestBridge_ConsoleCallsStillForward is the other half of the allowlist: the calls the
// console ACTUALLY makes must still reach commerce. An allowlist that blocks the product
// is not a fix, so each entry here is a live console call (cited in billing.go), and this
// test fails if a future edit narrows the table below the console's real needs.
func TestBridge_ConsoleCallsStillForward(t *testing.T) {
	calls := []struct{ method, path, want string }{
		{http.MethodGet, "/v1/billing/invoices", "/v1/billing/invoices"},
		{http.MethodGet, "/v1/billing/invoices/inv_123/pdf", "/v1/billing/invoices/inv_123/pdf"},
		{http.MethodGet, "/v1/billing/subscriptions", "/v1/billing/subscriptions"},
		{http.MethodGet, "/v1/billing/spend-alerts", "/v1/billing/spend-alerts"},
		{http.MethodGet, "/v1/billing/payment-config", "/v1/billing/payment-config"},
		{http.MethodGet, "/v1/billing/plans", "/v1/billing/plans"},
		{http.MethodGet, "/v1/billing/payouts", "/v1/billing/payouts"},
		{http.MethodPost, "/v1/billing/subscriptions/sub_1/cancel", "/v1/billing/subscriptions/sub_1/cancel"},
		{http.MethodPost, "/v1/billing/subscriptions/sub_1/reactivate", "/v1/billing/subscriptions/sub_1/reactivate"},
		{http.MethodPost, "/v1/billing/payment-methods", "/v1/billing/payment-methods"},
		{http.MethodPost, "/v1/billing/spend-alerts", "/v1/billing/spend-alerts"},
		{http.MethodPost, "/v1/billing/topup/token", "/v1/billing/topup/token"},
	}
	for _, call := range calls {
		t.Run(call.method+" "+call.path, func(t *testing.T) {
			f := &fakeBilling{}
			t.Setenv("COMMERCE_URL", f.server(t).URL)
			t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-tok")
			app := mountApp(t, "http://iam.invalid", "", "")

			code, body := callH(t, app, call.method, call.path, alice, "{}")
			if code != http.StatusOK {
				t.Fatalf("%s %s: want 200 (the console needs this), got %d (%s)",
					call.method, call.path, code, body)
			}
			if f.path != call.want {
				t.Fatalf("%s %s must forward to %q, got %q", call.method, call.path, call.want, f.path)
			}
		})
	}
}

// TestBridge_UnlistedPathsAreRefused covers the rest of the money surface — routes that
// are NOT mint-gated but that the console never calls. The bridge is not a general
// commerce proxy; least privilege means "only what the product needs", so these 404
// even though commerce would have served them to a service token.
func TestBridge_UnlistedPathsAreRefused(t *testing.T) {
	unlisted := []struct{ method, path string }{
		{http.MethodPost, "/v1/billing/invoices"},          // CreateInvoice (admin group)
		{http.MethodPost, "/v1/billing/invoices/i1/pay"},   // PayInvoice
		{http.MethodPost, "/v1/billing/invoices/i1/void"},  // VoidInvoice
		{http.MethodPost, "/v1/billing/meters"},            // CreateMeter
		{http.MethodPost, "/v1/billing/pricing-rules"},     // CreatePricingRule
		{http.MethodPost, "/v1/billing/withdraw"},          // money OUT
		{http.MethodPost, "/v1/billing/usage"},             // RecordUsage — the meter itself
		{http.MethodGet, "/v1/billing/balance/all"},        // every subject's balance
		{http.MethodGet, "/v1/billing/sbom"},               // OSS payout surface
		{http.MethodGet, "/v1/billing/oss-payout/summary"}, // OSS payout rollup
		{http.MethodPost, "/v1/billing/subscriptions"},     // CreateBillingSubscription
	}
	for _, u := range unlisted {
		t.Run(u.method+" "+u.path, func(t *testing.T) {
			f := &fakeBilling{}
			t.Setenv("COMMERCE_URL", f.server(t).URL)
			t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-tok")
			app := mountApp(t, "http://iam.invalid", "", "")

			code, _ := callH(t, app, u.method, u.path, alice, "{}")
			if f.path != "" {
				t.Fatalf("%s %s is not a console call and must not reach commerce, but upstream saw %q",
					u.method, u.path, f.path)
			}
			if code != http.StatusNotFound {
				t.Fatalf("%s %s: want 404, got %d", u.method, u.path, code)
			}
		})
	}
}

// TestBridge_ReadAllowlistIsNotAWriteAllowlist pins the method split. `payouts` is the
// proof that one method-blind set would be a hole: GET /payouts is a plain read, POST
// /payouts is `mintRequired` (api/billing/handlers.go). The same string must resolve
// differently by method, or reading the settlement view would grant minting a payout.
func TestBridge_ReadAllowlistIsNotAWriteAllowlist(t *testing.T) {
	if !isForwardableBilling(http.MethodGet, "payouts") {
		t.Fatal("GET payouts is a live console read and must be forwardable")
	}
	if isForwardableBilling(http.MethodPost, "payouts") {
		t.Fatal("POST payouts is mint-gated in commerce and must NEVER be forwardable")
	}
	// A GET-only entry must not leak into POST, and vice-versa.
	if isForwardableBilling(http.MethodPost, "invoices") {
		t.Fatal("POST invoices must not inherit the GET entry")
	}
	if isForwardableBilling(http.MethodGet, "topup/token") {
		t.Fatal("GET topup/token must not inherit the POST entry")
	}
	// An unknown method fails closed (the router mounts GET+POST only; defense in depth).
	for _, m := range []string{http.MethodPut, http.MethodPatch, http.MethodDelete, ""} {
		if isForwardableBilling(m, "balance") {
			t.Fatalf("method %q must fail closed", m)
		}
	}
	// `{}` matches exactly ONE segment — it can never swallow a path into a mint route.
	if isForwardableBilling(http.MethodPost, "subscriptions/a/b/cancel") {
		t.Fatal("{} must match exactly one segment")
	}
}

// TestBridge_StoreBridgeCannotReachBilling is the sibling lock. /v1/commerce/* carries the
// SAME admin token with FULL CRUD, and its own allowlist (commerceStoreHeads) is what keeps
// it a store proxy. Prove it cannot tunnel into the money surface — a store head that
// resolved to `billing` would reopen this hole from the other bridge.
func TestBridge_StoreBridgeCannotReachBilling(t *testing.T) {
	for _, p := range []string{
		"/v1/commerce/billing/deposit",
		"/v1/commerce/billing",
		"/v1/commerce/checkout",
		"/v1/commerce/_/commerce/tenants",
	} {
		f := &fakeBilling{}
		t.Setenv("COMMERCE_URL", f.server(t).URL)
		t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-tok")
		app := mountApp(t, "http://iam.invalid", "", "")

		code, _ := callH(t, app, http.MethodPost, p, alice, `{"amount":100000000}`)
		if f.path != "" {
			t.Fatalf("store bridge %q must never reach commerce, but upstream saw %q", p, f.path)
		}
		if code != http.StatusNotFound {
			t.Fatalf("store bridge %q: want 404, got %d", p, code)
		}
	}
}

// TestBridge_MintIsRefusedEvenWithForgedSubject proves the refusal does not depend
// on the subject-pinning. Pinning is an IDOR control, not an authority control: it
// makes the mint land on the CALLER's own account, which is the attack, not a
// defense. The path gate must refuse before any of that logic runs.
func TestBridge_MintIsRefusedEvenWithForgedSubject(t *testing.T) {
	f := &fakeBilling{}
	t.Setenv("COMMERCE_URL", f.server(t).URL)
	t.Setenv("COMMERCE_SERVICE_TOKEN", "svc-tok")
	app := mountApp(t, "http://iam.invalid", "", "")

	code, _ := callH(t, app, http.MethodPost, "/v1/billing/deposit", alice,
		`{"user":"victim","userId":"victim","amount":100000000}`)
	if f.path != "" {
		t.Fatalf("ESCALATION: deposit reached commerce at %q with the service token", f.path)
	}
	if code != http.StatusNotFound {
		t.Fatalf("forged-subject deposit: want 404, got %d", code)
	}
}
