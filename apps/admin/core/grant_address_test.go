package core

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud/apps/principal"
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
		w := principal.PayerFor(tc.org, tc.user)
		ok := !w.Zero()
		if ok != tc.ok {
			t.Errorf("PayerFor(%q,%q) ok = %v, want %v", tc.org, tc.user, ok, tc.ok)
			continue
		}
		if !ok {
			continue
		}
		if w.Subject() != tc.want {
			t.Errorf("PayerFor(%q,%q) account = %q, want %q", tc.org, tc.user, w.Subject(), tc.want)
		}
		// The two halves must come from one resolved Account, or a folded subject
		// could sit under an unfolded ledger and address a second file. Stated as
		// the relationship itself rather than by re-deriving the ledger through
		// Payer: Payer answers for a CREDENTIAL and refuses a nameless one in the
		// signup org, while a blank name here is a caller deliberately addressing
		// that org's account — so asking it would compare against the answer to a
		// different question.
		if w.Subject() != w.Org() && !strings.HasPrefix(w.Subject(), w.Org()+"/") {
			t.Errorf("WalletFor(%q,%q) = ledger %q, account %q — not two halves of one Account", tc.org, tc.user, w.Org(), w.Subject())
		}
	}
}

// refProbe runs grantRef inside a real request, with the operator nonce set to
// nonce ("" sends no header at all).
func refProbe(t *testing.T, subject, nonce string) string {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	var out string
	app.Get("/k", func(c *zip.Ctx) error {
		out = grantRef(c, subject, "usd", "trial", 500)
		return c.JSON(200, map[string]string{"ref": out})
	})
	req := httptest.NewRequest("GET", "/k", nil)
	if nonce != "" {
		req.Header.Set("Idempotency-Key", nonce)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("ref probe: %v", err)
	}
	_ = resp.Body.Close()
	return out
}

// TestGrantRefBindsTheSubject: two members of one org, same operator nonce, same
// amount, are DIFFERENT grants. Hashing the org alone made the second dedupe away
// against the first — a credit silently dropped, which on a money path is
// indistinguishable from theft.
func TestGrantRefBindsTheSubject(t *testing.T) {
	key := func(subject string) string { return refProbe(t, subject, "one-nonce") }
	alice, bob, pool := key("hanzo/alice"), key("hanzo/bob"), key("hanzo")
	if alice == "" {
		t.Fatal("an operator nonce must produce a ref")
	}
	if alice == bob || alice == pool || bob == pool {
		t.Fatalf("distinct addresses collided: alice=%s bob=%s pool=%s — the second grant dedupes away", alice, bob, pool)
	}
	if again := key("hanzo/alice"); again != alice {
		t.Fatalf("the same grant retried produced a different ref (%s != %s) — a retry double-credits", again, alice)
	}
}

// TestGrantRefIsAlwaysPresentAndAdditiveWithoutANonce pins the property the plane
// leg depends on and the co-resident leg is indifferent to.
//
// client.CreditIn.Ref is REQUIRED — commerce refuses an empty one, because an op that
// CREATES money and can be replayed is a money printer — so a grant that answered ""
// here could not be credited over the plane at all. It must therefore always produce
// a ref. And with no operator nonce it must produce a DIFFERENT one every attempt:
// that is what "additive" means, and a ref that were stable across attempts would
// silently drop the second of two legitimate identical comps.
func TestGrantRefIsAlwaysPresentAndAdditiveWithoutANonce(t *testing.T) {
	first, second := refProbe(t, "hanzo/alice", ""), refProbe(t, "hanzo/alice", "")
	if first == "" || second == "" {
		t.Fatal("a grant with no operator nonce produced no ref — commerce refuses an empty ref, so it could not be credited over the plane")
	}
	if first == second {
		t.Fatalf("two attempts with no operator nonce produced ONE ref (%s) — the second grant would dedupe away", first)
	}
}
