// Copyright © 2026 Hanzo AI. MIT License.

package apps

// starter_test.go proves the one claim a starter grant lives or dies on: the money
// lands at the address the SPEND GATE READS. A grant that credits a wallet the gate
// does not read is worse than no grant — the account looks funded and still 402s, and
// that exact bug has shipped twice before (clients/principal/wallet.go names both).
//
// So these tests never assert against the balance the grant itself reports. They
// re-derive the gate's address the way the gate does — account.Payer(...).Subject(),
// the ONE rule — and read THAT. If the two ever diverge, the read returns 0 and every
// test here fails.
//
// It lives in package apps because this is where the real chain is assembled: the
// cloud ledger adapter (ledger{}) that translates (Org, Subject) into a finance
// posting. Testing account.EnsureStarterCredit against a stub ledger would prove only
// that the stub agrees with itself.

import (
	"context"
	"sync"
	"testing"

	"github.com/hanzoai/account"
	accountclient "github.com/hanzoai/cloud/clients/account"
	"github.com/hanzoai/cloud/clients/finance"
	commercecredit "github.com/hanzoai/commerce/billing/credit"
	"github.com/hanzoai/commerce/billing/creditledger"
)

// wireLedger assembles the production money chain over a temp data dir: a real
// finance ledger, published as the process-wide client, with the real cloud adapter
// injected as commerce's credit seam. This is the same pair build.go/apps wire at
// boot — no fakes anywhere in the path under test.
func wireLedger(t *testing.T) finance.Client {
	t.Helper()
	fin := finance.New(t.TempDir())
	finance.Publish(fin)
	creditledger.Set(ledger{})
	t.Cleanup(func() {
		creditledger.Set(nil)
		finance.Publish(nil)
	})
	return fin
}

// gateBalance reads a principal's balance EXACTLY as the spend gate does: resolve the
// payer with account.Payer (what principal.WalletOf calls), then read that subject in
// the home org's ledger (what build.go's BalanceReader and metering's fetchAvailable
// both call). Owner is the ledger, name the person.
//
// No `billing_account` claim is supplied because IAM v2 mints none — so Payer takes
// the fallback, which is precisely the production shape.
func gateBalance(t *testing.T, fin finance.Client, owner, name string) int64 {
	t.Helper()
	subject := account.Payer(account.Credential{Owner: owner, Name: name}).Subject()
	bal, err := fin.Balance(context.Background(), owner, subject, "usd", false)
	if err != nil {
		t.Fatalf("gate balance read (owner=%q name=%q subject=%q): %v", owner, name, subject, err)
	}
	return bal.Cents()
}

// TestStarterCredit_LandsAtTheAddressTheGateReads is THE test. A freshly onboarded org
// is granted, and the balance is then read through the gate's own address rule — not
// the grant's return value. They must agree, or the grant funds a wallet nobody spends
// from.
func TestStarterCredit_LandsAtTheAddressTheGateReads(t *testing.T) {
	fin := wireLedger(t)

	if got := gateBalance(t, fin, "acme", "alice"); got != 0 {
		t.Fatalf("a brand-new org must start at zero, got %d cents", got)
	}

	reported, err := accountclient.EnsureStarterCredit(context.Background(), "acme")
	if err != nil {
		t.Fatalf("EnsureStarterCredit: %v", err)
	}

	// The founder reads the grant.
	if got := gateBalance(t, fin, "acme", "alice"); got != commercecredit.StarterCreditCents {
		t.Fatalf("the GATE reads %d cents at acme/alice, want %d — the grant landed at an address the gate does not read",
			got, commercecredit.StarterCreditCents)
	}
	// A second member of the same org reads the SAME pool: one grant funds the tenant,
	// not each employee. This is what makes "once per account" mean once per org.
	if got := gateBalance(t, fin, "acme", "bob"); got != commercecredit.StarterCreditCents {
		t.Fatalf("a second member reads %d cents, want the same pool %d", got, commercecredit.StarterCreditCents)
	}
	if reported != commercecredit.StarterCreditCents {
		t.Fatalf("reported balance %d, want %d", reported, commercecredit.StarterCreditCents)
	}
}

// TestStarterCredit_AmountIsServerAuthoritative proves the granted amount is the
// server's shared constant. EnsureStarterCredit takes no amount parameter at all, so
// there is no client field that could reach it — this pins the value against drift
// between the constant and what actually lands.
func TestStarterCredit_AmountIsServerAuthoritative(t *testing.T) {
	fin := wireLedger(t)
	if _, err := accountclient.EnsureStarterCredit(context.Background(), "acme"); err != nil {
		t.Fatalf("EnsureStarterCredit: %v", err)
	}
	if got := gateBalance(t, fin, "acme", "alice"); got != 500 {
		t.Fatalf("granted %d cents, want exactly 500 ($5.00, the one canonical amount)", got)
	}
}

// TestStarterCredit_RetryGrantsOnce covers the sequential replays: a retried request,
// a double-submitted form, a re-login, a redeploy that re-runs onboarding. Every one
// derives the same address-keyed ref, so the ledger credits once.
func TestStarterCredit_RetryGrantsOnce(t *testing.T) {
	fin := wireLedger(t)
	for i := 0; i < 5; i++ {
		if _, err := accountclient.EnsureStarterCredit(context.Background(), "acme"); err != nil {
			t.Fatalf("EnsureStarterCredit call %d: %v", i, err)
		}
	}
	if got := gateBalance(t, fin, "acme", "alice"); got != commercecredit.StarterCreditCents {
		t.Fatalf("after 5 grants the gate reads %d cents, want %d — the grant stacked",
			got, commercecredit.StarterCreditCents)
	}
}

// TestStarterCredit_ConcurrentGrantsOnce is the one that matters for money. A
// sequential retry test passes against a read-then-write guard that is still racy;
// only concurrency distinguishes a real idempotency barrier from a checked one.
// Twenty goroutines start together and grant the same org.
//
// The barrier under test is finance.Deposit's dedup on Ref, which does its
// EntryByRef check INSIDE the same transaction as the insert, over a store pinned to
// SetMaxOpenConns(1). If that ever loosens — a second connection, a deferred
// transaction, a check moved out of the tx — this test turns the regression into a
// balance that is a multiple of $5.
func TestStarterCredit_ConcurrentGrantsOnce(t *testing.T) {
	fin := wireLedger(t)

	const racers = 20
	var start sync.WaitGroup
	var done sync.WaitGroup
	start.Add(1)
	errs := make([]error, racers)
	for i := 0; i < racers; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait() // release all goroutines at once
			_, errs[i] = accountclient.EnsureStarterCredit(context.Background(), "acme")
		}(i)
	}
	start.Done()
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent grant %d failed: %v", i, err)
		}
	}
	if got := gateBalance(t, fin, "acme", "alice"); got != commercecredit.StarterCreditCents {
		t.Fatalf("after %d CONCURRENT grants the gate reads %d cents, want %d — the idempotency key is not a barrier under race",
			racers, got, commercecredit.StarterCreditCents)
	}
}

// TestStarterCredit_PerOrgNotPerProcess proves the key is scoped to the account: two
// different orgs each get their own grant. An over-broad key would fund the first org
// and silently skip every one after it.
func TestStarterCredit_PerOrgNotPerProcess(t *testing.T) {
	fin := wireLedger(t)
	for _, org := range []string{"acme", "globex", "initech"} {
		if _, err := accountclient.EnsureStarterCredit(context.Background(), org); err != nil {
			t.Fatalf("EnsureStarterCredit(%s): %v", org, err)
		}
	}
	for _, org := range []string{"acme", "globex", "initech"} {
		if got := gateBalance(t, fin, org, "founder"); got != commercecredit.StarterCreditCents {
			t.Fatalf("org %s reads %d cents, want %d", org, got, commercecredit.StarterCreditCents)
		}
	}
}

// TestStarterCredit_PoolIsNotThePersonWallet is the NEGATIVE CONTROL for every other
// test here, and the reason the grant fires on first-run onboarding rather than on
// signup.
//
// In the shared signup org the members are strangers, so account.Payer resolves each
// to their OWN wallet, not the org pool. An org-keyed grant into that org therefore
// lands somewhere no member's gate reads. This test asserts that gap exists — it is
// what makes the passing address tests above meaningful rather than vacuous, and it
// pins the trap so nobody "simplifies" the trigger into granting at signup.
func TestStarterCredit_PoolIsNotThePersonWallet(t *testing.T) {
	fin := wireLedger(t)

	// account.SignupOrg is the shared org every self-serve signup lands in.
	if _, err := accountclient.EnsureStarterCredit(context.Background(), account.SignupOrg); err != nil {
		t.Fatalf("EnsureStarterCredit(%s): %v", account.SignupOrg, err)
	}

	// A member of the signup org reads their PERSON wallet — and it is EMPTY, because
	// the grant went to the pool. This is the "looks fixed and 402s anyway" failure.
	if got := gateBalance(t, fin, account.SignupOrg, "alice"); got != 0 {
		t.Fatalf("a signup-org member reads %d cents from an org-keyed grant; the test's address model is wrong", got)
	}
	// Same ledger, different account: the pool itself did receive the money, so the
	// zero above is an ADDRESS mismatch, not a failed write.
	pool, err := fin.Balance(context.Background(), account.SignupOrg, account.SignupOrg, "usd", false)
	if err != nil {
		t.Fatalf("pool balance: %v", err)
	}
	if pool.Cents() != commercecredit.StarterCreditCents {
		t.Fatalf("pool holds %d cents, want %d — the write itself failed, so this proves nothing about addressing",
			pool.Cents(), commercecredit.StarterCreditCents)
	}
}

// TestStarterCredit_NoLedgerIsAnError proves a missing ledger is reported, never
// silently treated as a successful grant. The caller logs it and the account stays at
// $0 — which the gate refuses. A swallowed error here would be a grant that never
// happened but looked like it did.
func TestStarterCredit_NoLedgerIsAnError(t *testing.T) {
	creditledger.Set(nil)
	finance.Publish(nil)
	if _, err := accountclient.EnsureStarterCredit(context.Background(), "acme"); err == nil {
		t.Fatal("EnsureStarterCredit with no ledger returned nil error — a grant that did not happen must not report success")
	}
}

// TestStarterCredit_EmptyOrgRefused proves an unnamed account is refused rather than
// credited to a wallet nobody owns.
func TestStarterCredit_EmptyOrgRefused(t *testing.T) {
	wireLedger(t)
	for _, org := range []string{"", "   "} {
		if _, err := accountclient.EnsureStarterCredit(context.Background(), org); err == nil {
			t.Fatalf("EnsureStarterCredit(%q) returned nil error — an unnamed account must be refused", org)
		}
	}
}
