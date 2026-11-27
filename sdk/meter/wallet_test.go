package meter_test

import (
	"context"
	"encoding/json"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/account"
	"github.com/zap-proto/zip"
)

// wallet drives principal.Payer over the SAME zip.Ctx header accessors the
// identity boundary feeds in production, so a test sets X-User-Id / X-User-Name /
// X-Org-Id exactly as SanitizeIdentity mints them: X-User-Id from the JWT `sub`
// (a UUID), X-User-Name from the validated `name` claim (the IAM username).
func wallet(t *testing.T, headers map[string]string) (ledger, acct string, ok bool) {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Raw(http.MethodGet, "/w", func(c *zip.Ctx) error {
		w := meter.Payer(c)
		return c.JSON(200, map[string]any{"ledger": w.Org(), "account": w.Subject(), "ok": !w.Zero()})
	})
	req := httptest.NewRequest("GET", "/w", nil)
	for h, v := range headers {
		req.Header.Set(h, v)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("wallet probe: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Ledger  string `json:"ledger"`
		Account string `json:"account"`
		OK      bool   `json:"ok"`
	}
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("wallet decode: %v (%s)", err, b)
	}
	return out.Ledger, out.Account, out.OK
}

// signupPerson is the live shape of a self-serve account: home org == the shared
// signup org, X-User-Id == the JWT `sub` (a UUID), X-User-Name == the IAM username.
var signupPerson = map[string]string{
	"X-User-Id":     "3f2a9c14-7b6e-4d05-9a11-8c73e2f0b4d6",
	"X-User-Name":   "z@hanzo.ai",
	"X-Org-Id":      account.SignupOrg,
	"X-User-Owner":  account.SignupOrg,
	"Authorization": "Bearer test",
}

// TestWallet_AddressIsTheFundableOne is the deliverable. The wallet a money gate
// addresses must be the wallet a deposit can NAME — and every funding path names
// "<org>/<username>" or the bare org, never "<org>/<uuid>".
//
// The regression: the wallet read X-User-Id for the name half. Both minters set that
// header from the JWT `sub`, and IAM's `sub` is a UUID, so the address resolved to
// "hanzo/<uuid>" — a wallet no grant, no promo, no starter credit and no operator
// deposit can ever reach. It read $0 forever while the ai gate, the usage debit and
// GET /v1/billing/balance all addressed "hanzo/z@hanzo.ai". Third recurrence of one
// bug: two layers deriving one address two ways.
func TestWallet_AddressIsTheFundableOne(t *testing.T) {
	ledger, acct, ok := wallet(t, signupPerson)
	if !ok {
		t.Fatalf("validated signup person resolved no wallet")
	}
	if ledger != account.SignupOrg {
		t.Fatalf("ledger = %q, want %q", ledger, account.SignupOrg)
	}
	want := account.Payer(account.Credential{Owner: account.SignupOrg, Name: "z@hanzo.ai"}).Subject()
	if acct != want {
		t.Fatalf("account = %q, want %q (the address every funding path names)", acct, want)
	}
	if acct == account.SignupOrg+"/"+signupPerson["X-User-Id"] {
		t.Fatalf("account is the UUID ghost %q — unfundable by construction", acct)
	}
}

// TestWallet_AgreesWithTheGate: the wallet the edge gate + paywall address must be
// byte-identical to the subject the ai prepaid gate and the usage debit resolve from
// the SAME credential. ai keys on the IAM (owner, name) pair through account.Payer;
// so must this. They are one function, so the proof is that both are fed the same
// two values — the point of the test is that the HEADERS they are read from agree.
func TestWallet_AgreesWithTheGate(t *testing.T) {
	_, acct, _ := wallet(t, signupPerson)
	aiSubject := account.Payer(account.Credential{
		Owner: signupPerson["X-Org-Id"], Name: signupPerson["X-User-Name"],
	}).Subject()
	if acct != aiSubject {
		t.Fatalf("edge wallet %q != ai gate subject %q — a funded caller 402s, or an empty one serves", acct, aiSubject)
	}
}

// TestWallet_SignedClaimWins: when IAM names the payer, the claim decides — the
// same rule ai's resolveBillingKey applies. A signup-org member whose token says
// `org:hanzo` pools; the inference alone would have split them per-person.
func TestWallet_AccountClaimWins(t *testing.T) {
	h := map[string]string{}
	maps.Copy(h, signupPerson)
	h["X-Billing-Account-Id"] = "org:" + account.SignupOrg
	_, acct, ok := wallet(t, h)
	if !ok {
		t.Fatalf("claimed principal resolved no wallet")
	}
	if acct != account.SignupOrg {
		t.Fatalf("account = %q, want %q (the signed claim must beat the personal inference)", acct, account.SignupOrg)
	}
}

// TestWallet_PooledOrgStaysPooled: a member of a REAL org spends the org's one
// balance. The username must not split them onto a personal wallet nobody funds.
func TestWallet_PooledOrgStaysPooled(t *testing.T) {
	_, acct, ok := wallet(t, map[string]string{
		"X-User-Id":    "0d1e2f30-4a5b-6c7d-8e9f-a0b1c2d3e4f5",
		"X-User-Name":  "bob",
		"X-Org-Id":     "acme",
		"X-User-Owner": "acme",
	})
	if !ok {
		t.Fatalf("validated org member resolved no wallet")
	}
	if acct != "acme" {
		t.Fatalf("account = %q, want %q (a real org pools)", acct, "acme")
	}
}

// TestWallet_IdShapesResolveOneName: with no minted username the id is the only
// name available, and it arrives in two nameable shapes across the fleet's paths.
// Both must reduce to the SAME name, or one human holds two wallets. (The third
// shape, the UUID `sub`, names no username at all — TestWallet_AddressIsTheFundableOne
// is why the minted X-User-Name must win over it whenever it is present.)
func TestWallet_IdShapesResolveOneName(t *testing.T) {
	for _, id := range []string{
		"alice",                      // the gateway's historical mint: the bare username
		account.SignupOrg + "/alice", // the key form callers hold
	} {
		_, acct, ok := wallet(t, map[string]string{
			"X-User-Id":    id,
			"X-Org-Id":     account.SignupOrg,
			"X-User-Owner": account.SignupOrg,
		})
		if !ok {
			t.Fatalf("id %q: validated principal resolved no wallet", id)
		}
		if want := account.SignupOrg + "/alice"; acct != want {
			t.Fatalf("id %q: account = %q, want %q", id, acct, want)
		}
	}
}

// TestWallet_ClaimSurvivesAMissingUsername is the regression this fix nearly
// introduced. A two-branch resolver that shortcuts to account.PayerOf when
// X-User-Name is absent DROPS the signed `billing_account` claim, and a token whose
// signature says "person:acme/bob" would gate the acme POOL instead. The claim is
// the whole point of the module: it cannot be conditional on a header's shape.
func TestWallet_ClaimSurvivesAMissingUsername(t *testing.T) {
	_, acct, ok := wallet(t, map[string]string{
		"X-User-Id":            "bob",
		"X-Org-Id":             "acme",
		"X-User-Owner":         "acme",
		"X-Billing-Account-Id": "person:acme/bob",
	})
	if !ok {
		t.Fatalf("claimed principal resolved no wallet")
	}
	if acct != "acme/bob" {
		t.Fatalf("account = %q, want %q (the signature must beat the pooled inference)", acct, "acme/bob")
	}
}

// TestWallet_UnvalidatedRefuses: no validated principal ⟹ no wallet. An anonymous
// caller must never be able to name a ledger and probe or drain it.
func TestWallet_UnvalidatedRefuses(t *testing.T) {
	if _, _, ok := wallet(t, map[string]string{"X-Org-Id": "victim"}); ok {
		t.Fatalf("forged X-Org-Id with no principal resolved a wallet")
	}
}

// A SUPERADMIN INSPECTING SOMEBODY ELSE'S ORG SPENDS ITS OWN BOOKS, and the
// context shape has to agree with the request shape or the two entry points bill
// different people.
//
// Payer reads a request, PayerFrom reads a context, and both exist because a core
// that takes identity as arguments cannot hold a *zip.Ctx. The rule they share is
// one line, so the risk was never that the rule is wrong — it is that a second copy
// of it drifts. This drives the context shape through every case, including the one
// the whole distinction exists for: apps/sandbox billed the EFFECTIVE org, so an
// operator leasing a sandbox while inspecting a customer charged the customer — and
// the one the ledger-string form could not express: in the signup org the person
// pays, never the pool beside them.
func TestPayerFrom_MasqueradeSpendsTheOperatorsOwnBooks(t *testing.T) {
	for _, tc := range []struct {
		name       string
		org, owner string
		user       string
		super      bool
		want       account.Account
	}{
		{"a member acting at home", "acme", "acme", "u-1", false, account.Org("acme")},
		{"a member whose home differs — the SELECTED org still pays", "acme", "other", "u-1", false, account.Org("acme")},
		{"a SuperAdmin at home", "admin", "admin", "u-1", true, account.Org("admin")},
		{"a SuperAdmin inspecting acme — admin pays, NOT acme", "acme", "admin", "u-1", true, account.Org("admin")},
		{"a SuperAdmin with no home claim falls back to the org", "acme", "", "u-1", true, account.Org("acme")},
		{"a person in the signup org pays from their own wallet", account.SignupOrg, account.SignupOrg, "alice", false, account.Person(account.SignupOrg, "alice")},
		{"no org resolves to no payer, so nothing is billed", "", "admin", "u-1", true, account.Account{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := zip.WithCaller(context.Background(), zip.Caller{
				Org: tc.org, Owner: tc.owner, Admin: tc.super, User: tc.user,
			})
			if got := meter.PayerFrom(ctx); got != tc.want {
				t.Fatalf("PayerFrom(org=%q owner=%q user=%q super=%v) = %q, want %q",
					tc.org, tc.owner, tc.user, tc.super, got.Subject(), tc.want.Subject())
			}
		})
	}
}

// A stated caller carries the signed billing claim (zip.Caller.Account), so work
// detached from its request pays from the account the request named: an org owner
// in the signup org from the org's pool, as on the HTTP path. A claim naming
// another org's ledger is not honored, so a stated claim cannot reach a stranger's
// balance.
func TestPayerFrom_TheStatedClaimDecides(t *testing.T) {
	for _, tc := range []struct {
		name  string
		claim string
		want  account.Account
	}{
		{"no claim: the person in the signup org pays", "", account.Person(account.SignupOrg, "z")},
		{"a claim naming the org pool pays from the pool", "org:" + account.SignupOrg, account.Org(account.SignupOrg)},
		{"a claim naming another org is not honored", "org:victim", account.Person(account.SignupOrg, "z")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := zip.WithCaller(context.Background(), zip.Caller{
				Org: account.SignupOrg, Owner: account.SignupOrg, User: "z", Account: tc.claim,
			})
			if got := meter.PayerFrom(ctx); got != tc.want {
				t.Fatalf("PayerFrom(claim=%q) = %q, want %q", tc.claim, got.Subject(), tc.want.Subject())
			}
		})
	}
}

// TestPayer_TracksBillingOrg: the account every meter is handed keeps its books in
// the org BillingOrg names, or the debit and the gate would key different ledgers.
func TestPayer_TracksBillingOrg(t *testing.T) {
	for _, h := range []map[string]string{
		{"X-User-Id": "u-1", "X-Org-Id": "beta", "X-User-Owner": "acme"},
		{"X-User-Id": "u-z", "X-Org-Id": "maxpower", "X-User-Owner": "admin", "X-User-IsAdmin": "true"},
		{"X-Org-Id": "victim"},
	} {
		var billing, home string
		var ok bool
		app := zip.New(zip.Config{})
		app.Raw(http.MethodGet, "/who", func(c *zip.Ctx) error {
			billing, ok = principal.BillingOrg(c)
			home = meter.Payer(c).Org()
			return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
		})
		req := httptest.NewRequest(http.MethodGet, "/who", nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		if _, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); err != nil {
			t.Fatalf("Test request: %v", err)
		}
		want := billing
		if !ok {
			want = "" // an unbillable request names no payer at all.
		}
		if home != want {
			t.Fatalf("Payer's books = %q but BillingOrg = (%q, %v) for %v", home, billing, ok, h)
		}
	}
}
