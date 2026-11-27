package finance

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/money"
)

// RED729B-F1 (LOW, liveness): the deposit hook is told at Deposit, which the PR calls "the one place a
// wallet's balance rises". Refund (a payment given back), Reverse, Restore and Return (a hold released)
// also put money back in a wallet and tell nobody, so a run parked for payment waits for a Continue
// after a refund, and after finance.SetLimit raises an on-account line.
//
// MUTATION: leave the call out of Refund and a refunded payment tells nobody.
func TestRed729bARefundPutsMoneyBackAndTellsNobody(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	if _, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(5000), Ref: "promo"}); err != nil {
		t.Fatal(err)
	}
	plan := money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(2500), Ref: "record:acme:team:1"}
	if _, _, err := f.Draw(ctx, plan); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	told := 0
	SetDepositHook(func(string) { mu.Lock(); told++; mu.Unlock() })
	t.Cleanup(func() { SetDepositHook(nil) })
	if id, err := f.Refund(ctx, plan); err != nil || id == "" {
		t.Fatalf("refund = %q, %v", id, err)
	}
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if told == 0 {
		t.Errorf("RED: a refund put $25 back in acme's wallet and the deployment was told %d times; a run parked for want of funds is not tried", told)
	}
}

// hooked counts the times the hook is told, and waits for them (it is told on a goroutine of its
// own).
type hooked struct {
	mu sync.Mutex
	n  int
}

func hook(t *testing.T) *hooked {
	t.Helper()
	h := &hooked{}
	SetDepositHook(func(string) { h.mu.Lock(); h.n++; h.mu.Unlock() })
	t.Cleanup(func() { SetDepositHook(nil) })
	return h
}

func (h *hooked) told(t *testing.T, want int, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		h.mu.Lock()
		n := h.n
		h.mu.Unlock()
		if n == want {
			time.Sleep(50 * time.Millisecond) // and not once more
			h.mu.Lock()
			n = h.n
			h.mu.Unlock()
			if n != want {
				t.Fatalf("%s: told %d times, want %d", what, n, want)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: told %d times, want %d", what, n, want)
		}
		time.Sleep(time.Millisecond)
	}
}

// A clawback the processor reversed is restored to the wallet, a hold is returned to it and a
// credit line is raised, and each tells the deployment the money is there; a reversal of a deposit
// takes money out and tells nobody. A refund that fails to settle and is retried tells when the
// retry settles.
//
// MUTATION: leave the call out of Restore, Return or SetLimit and that movement tells nobody;
// tell from Reverse and a reversal wakes runs for money that was taken away.
func TestMoneyReachingAWalletTellsWhoWasRefusedForWantOfIt(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	if _, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(5000), Ref: "pay_1", Cash: true}); err != nil {
		t.Fatal(err)
	}
	h := hook(t)

	claw := ClawbackInput{Org: "acme", Subject: "acme", Payment: "pay_1", Ref: "refund:rf_1", Amount: money.FromCents(3000), Reason: "refund.updated rf_1"}
	if id, err := f.Clawback(ctx, claw); err != nil || id == "" {
		t.Fatalf("clawback = %q, %v", id, err)
	}
	h.told(t, 0, "a clawback takes money out")
	if id, err := f.Restore(ctx, claw); err != nil || id == "" {
		t.Fatalf("restore = %q, %v", id, err)
	}
	h.told(t, 1, "a restore")

	job := money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(700), Ref: "x402_1"}
	if _, _, err := f.Hold(ctx, job); err != nil {
		t.Fatal(err)
	}
	h.told(t, 1, "a hold takes money aside")
	if back, err := f.Return(ctx, job); err != nil || back.Cents() != 700 {
		t.Fatalf("return = %s, %v", back, err)
	}
	h.told(t, 2, "a return of a hold")
	if _, err := f.Return(ctx, job); err != nil {
		t.Fatal(err)
	}
	h.told(t, 3, "a return already made is told again when it settles, as a refund is")

	if _, err := f.SetLimit(ctx, "acme", false, money.FromCents(10000)); err != nil {
		t.Fatal(err)
	}
	h.told(t, 4, "a credit line raised")

	dep, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(100), Ref: "small"})
	if err != nil {
		t.Fatal(err)
	}
	h.told(t, 5, "a deposit")
	if _, err := f.Reverse(ctx, ReverseInput{Org: "acme", Entry: dep, Reason: "mistake"}); err != nil {
		t.Fatal(err)
	}
	h.told(t, 5, "a reversal of a deposit takes the money out")
}

// A refund whose first call posted and failed to settle is retried by whoever asked; the retry
// settles it and tells, as a deposit's does.
//
// MUTATION: settle and tell only on the call that posted and the retried refund tells nothing.
func TestARefundRetriedAfterAFailedSettleTells(t *testing.T) {
	ctx := context.Background()
	books := &shaky{Ledgers: Local(t.TempDir(), testMaster)}
	books.failed.Store(true) // the deposit below settles
	f := New(books)
	defer func() { _ = f.Close() }()
	if _, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(5000), Ref: "promo"}); err != nil {
		t.Fatal(err)
	}
	plan := money.UsageInput{Org: "acme", Subject: "acme", Amount: money.FromCents(2500), Ref: "record:acme:team:1"}
	if _, _, err := f.Draw(ctx, plan); err != nil {
		t.Fatal(err)
	}
	h := hook(t)
	books.failed.Store(false) // the next acknowledgement fails once
	if _, err := f.Refund(ctx, plan); err == nil {
		t.Fatal("the first refund should have failed to settle")
	}
	h.told(t, 0, "a refund that could not settle")
	if id, err := f.Refund(ctx, plan); err != nil || id == "" {
		t.Fatalf("the retried refund = %q, %v", id, err)
	}
	h.told(t, 1, "the retried refund")
}

// A hook that panics fails nothing: the money has moved and is answered.
//
// MUTATION: call the hook on the money path without recovering and a listener that panics takes
// the process down with the refund.
func TestAHookThatPanicsDoesNotFailTheMoney(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	SetDepositHook(func(string) { panic("a listener that cannot cope") })
	t.Cleanup(func() { SetDepositHook(nil) })
	if _, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(500), Ref: "pay-1"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
}
