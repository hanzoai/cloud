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
// posting. Testing cloud.EnsureStarterCredit against a stub ledger would prove only
// that the stub agrees with itself. The middleware's own behaviour — the hot-path
// cache, which principals are eligible — is proven in middleware_starter_test.go,
// where a request context is cheap to build.

import (
	"context"
	"sync"
	"testing"

	"github.com/hanzoai/account"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/money"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/hanzoai/cloud/types"
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

// starterWalletOf builds the address a credential resolves to, by the SAME rule the gate
// uses. Owner is the home org (the ledger), name the person. No `billing_account`
// claim is supplied because IAM v2 mints none, so Payer takes the fallback — which is
// precisely the production shape.
func starterWalletOf(owner, name string) principal.Wallet {
	return principal.Wallet{
		Ledger:  owner,
		Account: account.Payer(account.Credential{Owner: owner, Name: name}).Subject(),
	}
}

// gateBalance reads a principal's balance EXACTLY as the spend gate does: resolve the
// payer with account.Payer (what principal.WalletOf calls), then read that subject in
// the home org's ledger (what build.go's BalanceReader and metering's fetchAvailable
// both call).
func gateBalance(t *testing.T, fin finance.Client, owner, name string) int64 {
	t.Helper()
	w := starterWalletOf(owner, name)
	bal, err := fin.Balance(context.Background(), w.Ledger, w.Account, "usd", false)
	if err != nil {
		t.Fatalf("gate balance read (owner=%q name=%q subject=%q): %v", owner, name, w.Account, err)
	}
	return bal.Cents()
}

// TestStarterCredit_LandsAtTheAddressTheGateReads is THE test. A new account is
// granted, and the balance is then read through the gate's own address rule — not the
// grant's return value. They must agree, or the grant funds a wallet nobody spends
// from.
func TestStarterCredit_LandsAtTheAddressTheGateReads(t *testing.T) {
	fin := wireLedger(t)

	if got := gateBalance(t, fin, "acme", "alice"); got != 0 {
		t.Fatalf("a brand-new org must start at zero, got %d cents", got)
	}

	reported, err := cloud.EnsureStarterCredit(context.Background(), starterWalletOf("acme", "alice"))
	if err != nil {
		t.Fatalf("EnsureStarterCredit: %v", err)
	}

	if got := gateBalance(t, fin, "acme", "alice"); got != commercecredit.StarterCreditCents {
		t.Fatalf("the GATE reads %d cents at acme, want %d — the grant landed at an address the gate does not read",
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

// TestStarterCredit_AmountIsServerAuthoritative pins the granted amount to the shared
// constant. EnsureStarterCredit takes no amount and no request body, so no client
// field can reach it — this catches drift between the constant and what lands.
func TestStarterCredit_AmountIsServerAuthoritative(t *testing.T) {
	fin := wireLedger(t)
	if _, err := cloud.EnsureStarterCredit(context.Background(), starterWalletOf("acme", "alice")); err != nil {
		t.Fatalf("EnsureStarterCredit: %v", err)
	}
	if got := gateBalance(t, fin, "acme", "alice"); got != 500 {
		t.Fatalf("granted %d cents, want exactly 500 ($5.00, the one canonical amount)", got)
	}
}

// TestStarterCredit_RetryGrantsOnce covers the sequential replays: a retried request,
// a re-login, a process restart that empties the hot-path cache. Every one derives the
// same address-keyed ref, so the ledger credits once.
func TestStarterCredit_RetryGrantsOnce(t *testing.T) {
	fin := wireLedger(t)
	for i := 0; i < 5; i++ {
		if _, err := cloud.EnsureStarterCredit(context.Background(), starterWalletOf("acme", "alice")); err != nil {
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
// Twenty goroutines start together and grant the same wallet.
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
			_, errs[i] = cloud.EnsureStarterCredit(context.Background(), starterWalletOf("acme", "alice"))
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

// TestStarterCredit_PerAccountNotPerProcess proves the key is scoped to the address:
// three different orgs each get their own grant. An over-broad key would fund the
// first and silently skip every one after it.
func TestStarterCredit_PerAccountNotPerProcess(t *testing.T) {
	fin := wireLedger(t)
	orgs := []string{"acme", "globex", "initech"}
	for _, org := range orgs {
		if _, err := cloud.EnsureStarterCredit(context.Background(), starterWalletOf(org, "founder")); err != nil {
			t.Fatalf("EnsureStarterCredit(%s): %v", org, err)
		}
	}
	for _, org := range orgs {
		if got := gateBalance(t, fin, org, "founder"); got != commercecredit.StarterCreditCents {
			t.Fatalf("org %s reads %d cents, want %d", org, got, commercecredit.StarterCreditCents)
		}
	}
}

// TestStarterCredit_FundedAccountIsNotGranted is the guard against a retroactive
// payout to the whole customer base. A first-contact trigger sees every EXISTING
// account on the first request after a deploy; if "unseen" meant "new", every funded
// org in the fleet would take a free $5.
func TestStarterCredit_FundedAccountIsNotGranted(t *testing.T) {
	fin := wireLedger(t)
	// An existing customer with money on the books.
	if _, err := fin.Deposit(context.Background(), types.DepositInput{
		Org: "acme", Subject: "acme", Amount: money.FromCents(2500), Currency: "usd",
		Notes: "prior top-up", Ref: "prior-topup-1",
	}); err != nil {
		t.Fatalf("seed deposit: %v", err)
	}
	if _, err := cloud.EnsureStarterCredit(context.Background(), starterWalletOf("acme", "alice")); err != nil {
		t.Fatalf("EnsureStarterCredit: %v", err)
	}
	if got := gateBalance(t, fin, "acme", "alice"); got != 2500 {
		t.Fatalf("funded account reads %d cents, want its original 2500 — a retroactive starter credit was paid", got)
	}
}

// TestStarterCredit_SpentAccountIsNotGranted is the other half of that guard, and the
// reason a zero balance alone is not the eligibility test. An org that spent its
// balance down to exactly zero reads $0 but is not new; only its usage history
// distinguishes it from a fresh signup.
func TestStarterCredit_SpentAccountIsNotGranted(t *testing.T) {
	fin := wireLedger(t)
	ctx := context.Background()
	if _, err := fin.Deposit(ctx, types.DepositInput{
		Org: "acme", Subject: "acme", Amount: money.FromCents(1000), Currency: "usd",
		Notes: "prior top-up", Ref: "prior-topup-1",
	}); err != nil {
		t.Fatalf("seed deposit: %v", err)
	}
	if err := fin.RecordUsage(ctx, types.UsageInput{
		Org: "acme", Subject: "acme", Amount: money.FromCents(1000), Currency: "usd", RequestID: "prior-usage-1",
	}); err != nil {
		t.Fatalf("seed usage: %v", err)
	}
	if got := gateBalance(t, fin, "acme", "alice"); got != 0 {
		t.Fatalf("precondition: spent-out org should read 0, got %d", got)
	}
	if _, err := cloud.EnsureStarterCredit(ctx, starterWalletOf("acme", "alice")); err != nil {
		t.Fatalf("EnsureStarterCredit: %v", err)
	}
	if got := gateBalance(t, fin, "acme", "alice"); got != 0 {
		t.Fatalf("a spent-out account was granted %d cents — zero balance was mistaken for a new account", got)
	}
}

// TestStarterCredit_PoolIsNotThePersonWallet is the NEGATIVE CONTROL for every other
// test here, and the reason the shared signup org is excluded from the grant.
//
// In that org the members are strangers, so account.Payer resolves each to their OWN
// wallet, not the org pool. An org-keyed grant there lands somewhere no member's gate
// reads. This test asserts that gap exists — it is what makes the passing address
// tests above meaningful rather than vacuous, and it pins the trap so nobody
// "simplifies" the exclusion away.
func TestStarterCredit_PoolIsNotThePersonWallet(t *testing.T) {
	fin := wireLedger(t)
	org := account.SignupOrg

	// Credit the POOL directly (what an org-keyed grant would do).
	if _, err := cloud.EnsureStarterCredit(context.Background(), principal.Wallet{Ledger: org, Account: org}); err != nil {
		t.Fatalf("EnsureStarterCredit(pool): %v", err)
	}
	// A member of that org reads their PERSON wallet — EMPTY. This is the
	// "looks fixed and 402s anyway" failure.
	if got := gateBalance(t, fin, org, "alice"); got != 0 {
		t.Fatalf("a signup-org member reads %d cents from a pool grant; the address model is wrong", got)
	}
	// Same ledger, different account: the pool DID receive it, so the zero above is an
	// ADDRESS mismatch, not a failed write.
	pool, err := fin.Balance(context.Background(), org, org, "usd", false)
	if err != nil {
		t.Fatalf("pool balance: %v", err)
	}
	if pool.Cents() != commercecredit.StarterCreditCents {
		t.Fatalf("pool holds %d cents, want %d — the write itself failed, so this proves nothing about addressing",
			pool.Cents(), commercecredit.StarterCreditCents)
	}
}

// TestStarterCredit_NoLedgerIsInert proves a split deploy is a no-op, not a crash and
// not a false success: with no co-resident ledger there is nothing to grant into, the
// account stays at $0, and the gate refuses it.
func TestStarterCredit_NoLedgerIsInert(t *testing.T) {
	creditledger.Set(nil)
	finance.Publish(nil)
	got, err := cloud.EnsureStarterCredit(context.Background(), starterWalletOf("acme", "alice"))
	if err != nil {
		t.Fatalf("no-ledger must be inert, got error: %v", err)
	}
	if got != 0 {
		t.Fatalf("no-ledger reported balance %d, want 0", got)
	}
}
