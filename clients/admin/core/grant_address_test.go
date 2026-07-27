package core

import (
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// A grant is money entering a wallet, so it has the same address a spend does, and
// it must be resolved by the same rule — otherwise credit accumulates at an address
// no gate reads while the member it was meant for is refused at $0.

// TestGrantAddressIsTheSpendAddress: whatever a grant names, the account it lands on
// is account.Payer's answer — the very account that member's own requests are gated
// on. A pooled tenant org keeps ONE balance no matter which member is named, because
// in a pooled org there is no member wallet and money put in one could never be spent.
func TestGrantAddressIsTheSpendAddress(t *testing.T) {
	for _, tc := range []struct {
		org, user string
		want      string
		ok        bool
	}{
		{account.SignupOrg, "", account.SignupOrg, true},                           // the signup org itself: the pool
		{account.SignupOrg, "z@hanzo.ai", account.SignupOrg + "/z@hanzo.ai", true}, // a stranger in the shared org: their own wallet
		{"acme", "", "acme", true},                                                 // a tenant org: the pool
		{"acme", "bob", "acme", true},                                              // a member of a POOLED org: still the pool
		{"ACME", "Bob", "acme", true},                                              // folded, so one wallet not two
		{"", "bob", "", false},                                                     // no org, no address — never mint an orphan
		{account.SignupOrg, "hanzo/alice", "", false},                              // a key is not a name; refuse rather than address something else
	} {
		w, ok := principal.WalletFor(tc.org, tc.user)
		if ok != tc.ok {
			t.Errorf("WalletFor(%q,%q) ok = %v, want %v", tc.org, tc.user, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if w.Account != tc.want {
			t.Errorf("WalletFor(%q,%q) account = %q, want %q", tc.org, tc.user, w.Account, tc.want)
		}
		// The two halves must come from one resolved Account, or a folded subject
		// could sit under an unfolded ledger and address a second file.
		if w.Ledger != account.Payer(account.Credential{Owner: tc.org, Name: tc.user}).Org() {
			t.Errorf("WalletFor(%q,%q) ledger = %q — not the folded org the subject was built from", tc.org, tc.user, w.Ledger)
		}
	}
}

// TestGrantIdempotencyKeyBindsTheSubject: two members of one org, same operator
// nonce, same amount, are DIFFERENT grants. Hashing the org alone made the second
// dedupe away against the first — a credit silently dropped, which on a money path
// is indistinguishable from theft.
func TestGrantIdempotencyKeyBindsTheSubject(t *testing.T) {
	key := func(subject string) string {
		app := zip.New(zip.Config{DisableStartupMessage: true})
		var out string
		app.Get("/k", func(c *zip.Ctx) error {
			out = grantIdempotencyKey(c, subject, "usd", "trial", 500)
			return c.JSON(200, map[string]string{"key": out})
		})
		req := httptest.NewRequest("GET", "/k", nil)
		req.Header.Set("Idempotency-Key", "one-nonce")
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("key probe: %v", err)
		}
		_ = resp.Body.Close()
		return out
	}
	alice, bob, pool := key("hanzo/alice"), key("hanzo/bob"), key("hanzo")
	if alice == "" {
		t.Fatal("an operator nonce must produce a key")
	}
	if alice == bob || alice == pool || bob == pool {
		t.Fatalf("distinct addresses collided: alice=%s bob=%s pool=%s — the second grant dedupes away", alice, bob, pool)
	}
	if again := key("hanzo/alice"); again != alice {
		t.Fatalf("the same grant retried produced a different key (%s != %s) — a retry double-credits", again, alice)
	}
}
