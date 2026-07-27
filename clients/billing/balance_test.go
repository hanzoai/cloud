package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/account"

	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/money"
	"github.com/hanzoai/cloud/types"
)

// fakeFinance is the co-resident finance ledger seam (types.FinanceClient). It records
// the exact (org, subject) the handler read so a test can prove WHICH wallet the console
// shows, and can be made to fail so a test can prove an unreadable balance is never
// rendered as zero.
type fakeFinance struct {
	wallets   map[string]int64 // "org|subject" -> cents
	usageRows []finance.UsageRow
	err       error
	gotOrg    string
	gotSubj   string
	calls     int
}

// ListUsage satisfies the optional co-resident usage-read capability coResidentUsage
// resolves; returns the seeded rows so a test can prove the usage view answers from the
// ledger instead of the self-dispatching commerce hop.
func (f *fakeFinance) ListUsage(context.Context, string, int) ([]finance.UsageRow, error) {
	return f.usageRows, f.err
}

func (f *fakeFinance) Balance(_ context.Context, org, subject, _ string, _ bool) (money.Amount, error) {
	f.calls++
	f.gotOrg, f.gotSubj = org, subject
	if f.err != nil {
		return money.Zero(), f.err
	}
	return money.FromCents(f.wallets[org+"|"+subject]), nil
}

func (f *fakeFinance) Deposit(context.Context, types.DepositInput) (string, error) { return "", nil }
func (f *fakeFinance) RecordUsage(context.Context, types.UsageInput) error         { return nil }
func (f *fakeFinance) SumUsageSince(context.Context, string, bool, int64) (int64, error) {
	return 0, nil
}

func publishFinance(t *testing.T, f *fakeFinance) {
	t.Helper()
	finance.Publish(f)
	t.Cleanup(func() { finance.Publish(nil) })
}

// TestBalance_ReadsFinanceLedgerNotCommerce is the regression for the live incident:
// GET /v1/billing/balance proxied to commerce at the SAME path, which re-entered THIS
// handler (the commerce transport dispatches the shared app by path; commerce's own billing
// routes are never registered in the co-resident binary), answered "sign in to view
// billing", and surfaced as "billing upstream status 500".
//
// Co-resident the balance now comes from the finance ledger — the wallet the ai gate
// actually reads — and commerce is NOT called at all, so there is nothing to self-dispatch.
func TestBalance_ReadsFinanceLedgerNotCommerce(t *testing.T) {
	fin := &fakeFinance{wallets: map[string]int64{"maxpower|maxpower": 6875}}
	publishFinance(t, fin)

	f := &fakeCommerce{status: 200, body: `{"balance":1,"holds":0,"available":1}`}
	app := mountApp(t, f.server(t).URL, "svc-token")

	code, body := call(t, app, http.MethodGet, "/v1/billing/balance", "maxpower/dave", "maxpower")
	if code != 200 {
		t.Fatalf("balance: want 200, got %d (%s)", code, body)
	}
	var got commerceBalance
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if got.Available != 6875 || got.Balance != 6875 {
		t.Fatalf("balance not read from the finance ledger: %s", body)
	}
	// The proof there is no self-dispatch: the commerce hop never happened.
	if f.gotPath != "" {
		t.Fatalf("commerce was called at %q — the self-dispatching proxy is back", f.gotPath)
	}
	if fin.gotOrg != "maxpower" {
		t.Fatalf("read org %q, want the validated principal's org", fin.gotOrg)
	}
}

// TestBalance_SubjectIsTheGateSubject pins the invariant this incident broke: the wallet
// the console SHOWS must be the wallet the ai prepaid gate READS. Both derive it from the
// one function, ai/object.Payer, so they cannot drift apart again — cloud keeping its own
// copy of this rule is exactly what let the console show a funded org while the gate
// refused the member.
func TestBalance_SubjectIsTheGateSubject(t *testing.T) {
	// The allowlists are gone; set them to values that WOULD have flipped the resolution
	// to prove they are inert — nothing reads them, the signup org still bills per-person.
	t.Setenv("PERSONAL_BILLING_ORGS", "")
	t.Setenv("ORG_BILLING_ORGS", "hanzo")

	// The gate's subject for this principal, from ai itself — not a value this test invents.
	// This is what routers/filter_balance.go resolveBillingKey computes from the JWT claims:
	// a person in the signup org bills their OWN account, hanzo/z.
	want := account.Payer(account.Credential{Owner: "hanzo", Name: "z"}).Subject()
	if want != "hanzo/z" {
		t.Fatalf("precondition: ai resolves a signup-org person to %q, want hanzo/z", want)
	}

	// Every identity shape a validated principal can arrive in must land on that ONE wallet.
	for _, tc := range []struct{ name, userName, userID string }{
		// Production: the identity boundary mints X-User-Name from the `name` claim.
		{"gateway mints X-User-Name", "z", "8f14e45f-ea1b-4c2a-9f3d-000000000001"},
		// In-binary direct-Bearer: X-User-Id is the UUID subject, X-User-Name carries the name.
		{"in-binary direct bearer", "z", "hanzo/z"},
		// No X-User-Name: the "<owner>/<name>" key form folds back via PayerOf.
		{"owner/name id, no X-User-Name", "", "hanzo/z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fin := &fakeFinance{wallets: map[string]int64{}}
			publishFinance(t, fin)
			app := mountApp(t, "", "")

			req := httptest.NewRequest(http.MethodGet, "/v1/billing/balance", nil)
			req.Header.Set("X-User-Id", tc.userID)
			req.Header.Set("X-Org-Id", "hanzo")
			if tc.userName != "" {
				req.Header.Set("X-User-Name", tc.userName)
			}
			resp, err := app.Fiber().Test(req)
			if err != nil {
				t.Fatalf("Test: %v", err)
			}
			_ = resp.Body.Close()

			if fin.calls == 0 {
				t.Fatal("finance ledger was never read")
			}
			if fin.gotSubj != want {
				t.Fatalf("console read wallet %q but the ai gate reads %q — the view and the gate disagree", fin.gotSubj, want)
			}
		})
	}
}

// TestBalance_UnreadableIsNotZero is the fail-closed guard. A balance that cannot be read
// is UNKNOWN, and unknown must never render as a zero balance (a fabricated "you're broke")
// nor as a fabricated positive. It surfaces as an upstream failure.
func TestBalance_UnreadableIsNotZero(t *testing.T) {
	publishFinance(t, &fakeFinance{err: fmt.Errorf("ledger open failed")})
	app := mountApp(t, "", "")

	code, body := call(t, app, http.MethodGet, "/v1/billing/balance", "maxpower/dave", "maxpower")
	if code != http.StatusBadGateway {
		t.Fatalf("unreadable balance: want 502, got %d (%s)", code, body)
	}
	// The body must be an honest error, never a balance object — a caller must not be able
	// to mistake "unknown" for "zero".
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if _, isBalance := got["available"]; isBalance {
		t.Fatalf("an unreadable balance was rendered as a balance object: %s", body)
	}
	if got["error"] == nil {
		t.Fatalf("want an honest error body, got: %s", body)
	}
}

// TestBalance_RequiresSignIn proves the browser-facing sign-in gate is intact: no
// validated principal ⇒ 401, and the ledger is never touched. A forged service-token
// header cannot reach a wallet, because the org comes from the validated principal only.
func TestBalance_RequiresSignIn(t *testing.T) {
	fin := &fakeFinance{wallets: map[string]int64{"hanzo|hanzo": 14953300}}
	publishFinance(t, fin)
	app := mountApp(t, "", "")

	// No principal at all.
	if code, _ := call(t, app, http.MethodGet, "/v1/billing/balance", "", ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous: want 401, got %d", code)
	}
	// A client-supplied X-Org-Id with NO validated user is not a principal either — this
	// is the off-cluster forge Red will try.
	if code, _ := call(t, app, http.MethodGet, "/v1/billing/balance", "", "hanzo"); code != http.StatusUnauthorized {
		t.Fatalf("forged org, no principal: want 401, got %d", code)
	}
	if fin.calls != 0 {
		t.Fatalf("the ledger was read %d times without a validated principal", fin.calls)
	}
}

// TestBalance_ScopedToCallerOrg proves tenant isolation: the org is taken from the
// VALIDATED principal, so a caller reads only its OWN org's ledger no matter what the
// request says. There is no client-supplied input that can widen it.
func TestBalance_ScopedToCallerOrg(t *testing.T) {
	fin := &fakeFinance{wallets: map[string]int64{
		"victim|victim":     999999,
		"attacker|attacker": 5,
	}}
	publishFinance(t, fin)
	app := mountApp(t, "", "")

	// Attacker is validated in its OWN org and tries to name the victim's org/subject
	// through every client-controlled channel the old proxy forwarded.
	code, body := call(t, app, http.MethodGet,
		"/v1/billing/balance?user=victim&userId=victim&customerId=victim&org=victim&currency=usd",
		"attacker/mallory", "attacker")
	if code != 200 {
		t.Fatalf("want 200, got %d (%s)", code, body)
	}
	if fin.gotOrg != "attacker" {
		t.Fatalf("cross-org read: ledger org = %q, want attacker", fin.gotOrg)
	}
	var got commerceBalance
	_ = json.Unmarshal(body, &got)
	if got.Available == 999999 {
		t.Fatalf("attacker read the victim's balance: %s", body)
	}
}
