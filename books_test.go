package cloud_test

// The customer ledger on two pods — the C-2 finding, measured.
//
// A per-org SQLite ledger is safe inside one process by construction: one
// connection, one writer, a transaction around every posting. None of that reaches
// across pods. Before this, the books opened with cek and a single-connection pool
// and shipped nowhere, so two replicas each held a copy, each posted into their own,
// and whichever wrote to the object store last replaced the other — entire. The
// entries only the loser held were not merged or reordered; they were gone, and the
// customers who made them had been told the charge succeeded.
//
// These tests hold two files.

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/finance"
	_ "github.com/hanzoai/cloud/internal/devmaster"
	"github.com/hanzoai/cloud/internal/twopod"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"
)

// deps is what a composition root is handed for one replica: its own volume, its
// own durability, and the fact that it is not alone.
func deps(p *twopod.Pod) cloud.Deps {
	return cloud.Deps{DataDir: p.Dir, Durable: p.Durable, Peers: true}
}

// TestADepositSurvivesTheDeathOfThePodThatTookIt is the whole of C-2.
//
// Money is deposited on the replica that owns the org. That replica is then
// rescheduled — its volume goes with it, which is what "not durable" means — and
// the successor opens the org from the object store. The balance has to be there.
//
// MUTATION: open the ledger with sqlstore.Open under the pod's own DataDir instead
// of through customerBooks (which is what this file's subject replaced) and the
// successor reports a balance of zero for money the customer paid.
func TestADepositSurvivesTheDeathOfThePodThatTookIt(t *testing.T) {
	f := twopod.New(t, "pod-a", "pod-b")
	ctx, org := context.Background(), "acme"

	took := f.Owner(org)
	fin := finance.New(cloud.CustomerBooks(deps(took)))
	paid := money.FromCents(2500)
	if _, err := fin.Deposit(ctx, types.DepositInput{
		Org: org, Subject: org, Amount: paid, Ref: "sq_payment_1",
	}); err != nil {
		t.Fatalf("deposit on the owner: %v", err)
	}

	// The pod goes. Nothing of its disk survives.
	f.Stop(took.ID)

	next := finance.New(cloud.CustomerBooks(deps(f.Owner(org))))
	bal, err := next.Balance(ctx, org, org, "usd", false)
	if err != nil {
		t.Fatalf("balance on the successor: %v", err)
	}
	if bal.Cmp(paid) != 0 {
		t.Fatalf("the successor reports %s for a customer who deposited %s — "+
			"the money went with the pod", bal, paid)
	}
}

// TestANonOwnerCannotAcknowledgeADeposit. The other half: a replica that does not
// hold the org's books must not tell a customer their money landed.
//
// It CAN write it — nothing stops a local SQLite transaction, and pretending
// otherwise is the mistake — so what has to hold is that the write is not
// ACKNOWLEDGED. The fenced ship is where that is decided, and finance refuses on it.
//
// MUTATION: make finance.settle ignore `acked` and this passes while the deposit
// sits in a file nobody will ever open again, reported to the customer as taken.
func TestANonOwnerCannotAcknowledgeADeposit(t *testing.T) {
	f := twopod.New(t, "pod-a", "pod-b")
	ctx, org := context.Background(), "acme"

	// The owner opens the books first, so the object exists and the lease is held —
	// which is what makes the other replica a NON-owner rather than merely the first
	// to arrive.
	owner := finance.New(cloud.CustomerBooks(deps(f.Owner(org))))
	if _, err := owner.Deposit(ctx, types.DepositInput{
		Org: org, Subject: org, Amount: money.FromCents(100), Ref: "sq_1",
	}); err != nil {
		t.Fatalf("deposit on the owner: %v", err)
	}

	other := finance.New(cloud.CustomerBooks(deps(f.Other(org))))
	_, err := other.Deposit(ctx, types.DepositInput{
		Org: org, Subject: org, Amount: money.FromCents(9900), Ref: "sq_2",
	})
	if err == nil {
		t.Fatal("a replica that does not hold the org's books reported a deposit taken")
	}

	// And the owner's books are untouched by it: the refused deposit is not money
	// that quietly appears later.
	bal, berr := owner.Balance(ctx, org, org, "usd", false)
	if berr != nil {
		t.Fatalf("balance: %v", berr)
	}
	if want := money.FromCents(100); bal.Cmp(want) != 0 {
		t.Fatalf("the owner's balance is %s after a refused deposit, want %s", bal, want)
	}
}

// TestAUsageDebitIsNotReportedWhenItCannotBeShipped. The debit is the per-request
// half and the one that runs constantly, so it gets the same rule and its own
// measurement: an unreachable object store means the charge is REFUSED, not
// silently kept.
//
// The direction matters. A charge refused and not applied is a retry. A charge
// applied and reported failed is money a customer cannot see, cannot dispute and
// will not be told about.
//
// MUTATION: drop the settle call from RecordUsageOnce and this passes with the
// debit sitting in a local file the successor never sees.
func TestAUsageDebitIsNotReportedWhenItCannotBeShipped(t *testing.T) {
	f := twopod.New(t, "pod-a")
	ctx, org := context.Background(), "acme"
	fin := finance.New(cloud.CustomerBooks(deps(f.Owner(org))))

	if _, err := fin.Deposit(ctx, types.DepositInput{
		Org: org, Subject: org, Amount: money.FromCents(5000), Ref: "sq_1",
	}); err != nil {
		t.Fatalf("deposit: %v", err)
	}

	f.Store.Down()
	_, _, err := fin.RecordUsageOnce(ctx, types.UsageInput{
		Org: org, Subject: org, Amount: money.FromCents(10), Ref: "call_1", Model: "zen",
	})
	if err == nil {
		t.Fatal("a debit that could not be made durable was reported as recorded")
	}
	f.Store.Up()
}

// TestADeposedOwnerCannotAcknowledgeADeposit isolates the half of the rule the two
// tests above cannot see.
//
// A NON-owner's ship fails with an error, so a client that checked only the error
// would look correct there. Being DEPOSED is different and is the case that
// actually happens on a rolling upgrade: this replica held the lease, the fleet
// re-elected the org while a request was in flight, and the ship is REFUSED at a
// stale round — `(acked=false, err=nil)`. No error at all. A client reading only
// the error tells the customer their deposit landed, and the successor's copy has
// never heard of it.
//
// MUTATION: drop the `if !acked` branch from finance.settle and this passes while
// the deposit exists only on a pod that is no longer serving the org.
func TestADeposedOwnerCannotAcknowledgeADeposit(t *testing.T) {
	f := twopod.New(t, "pod-a", "pod-b")
	ctx, org := context.Background(), "acme"

	was := f.Owner(org)
	held := finance.New(cloud.CustomerBooks(deps(was)))
	if _, err := held.Deposit(ctx, types.DepositInput{
		Org: org, Subject: org, Amount: money.FromCents(100), Ref: "sq_1",
	}); err != nil {
		t.Fatalf("deposit while owning: %v", err)
	}

	// The fleet re-elects. This replica keeps its handle and its old lease — which
	// is exactly what a pod mid-drain has — while the successor claims a strictly
	// higher round by opening the org.
	f.Stop(was.ID)
	next := finance.New(cloud.CustomerBooks(deps(f.Owner(org))))
	if _, err := next.Balance(ctx, org, org, "usd", false); err != nil {
		t.Fatalf("the successor could not open the org: %v", err)
	}

	// The deposed replica writes. Its transaction commits; its ship is fenced.
	_, err := held.Deposit(ctx, types.DepositInput{
		Org: org, Subject: org, Amount: money.FromCents(9900), Ref: "sq_2",
	})
	if err == nil {
		t.Fatal("a deposed replica reported a deposit taken — its ship was refused and it said yes anyway")
	}

	// And the money is not on the successor's books, which is the customer's view.
	bal, berr := next.Balance(ctx, org, org, "usd", false)
	if berr != nil {
		t.Fatalf("balance: %v", berr)
	}
	if want := money.FromCents(100); bal.Cmp(want) != 0 {
		t.Fatalf("the successor reports %s, want %s — a fenced deposit reached the books", bal, want)
	}
}
