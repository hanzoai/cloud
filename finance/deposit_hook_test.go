package finance

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/money"
)

// Money added to a wallet is told at the one place a balance rises: whatever paid it (a
// card, a grant, a comp), in whichever wallet of the org, once it is in the wallet and
// acknowledged. A deposit replayed under the ref it was posted under tells again when it
// settles (the call that posted it may have failed before it could), and a deposit that
// fails tells nothing.
//
// MUTATION: tell before the post or the settle and a failed deposit does; tell only on the
// call that posted and the retry of a deposit whose first settle failed tells nothing
// (TestADepositRetriedAfterAFailedSettleTells).
func TestADepositTellsWhoWasRefusedForWantOfIt(t *testing.T) {
	ctx := context.Background()
	f := New(Local(t.TempDir(), testMaster))
	defer func() { _ = f.Close() }()
	var mu sync.Mutex
	var told []string
	SetDepositHook(func(org string) {
		mu.Lock()
		told = append(told, org)
		mu.Unlock()
	})
	t.Cleanup(func() { SetDepositHook(nil) })
	heard := func(want int) []string {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for {
			mu.Lock()
			n, got := len(told), append([]string(nil), told...)
			mu.Unlock()
			if n >= want || time.Now().After(deadline) {
				return got
			}
			time.Sleep(time.Millisecond)
		}
	}

	if _, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme/bob", Amount: money.FromCents(500), Ref: "pay-1"}); err != nil {
		t.Fatal(err)
	}
	if got := heard(1); len(got) != 1 || got[0] != "acme" {
		t.Fatalf("a deposit told %v, want acme once", got)
	}
	// The same payment again settles and tells again; a deposit that is refused tells
	// nothing, and neither does one for another payment under a taken ref.
	if _, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme/bob", Amount: money.FromCents(500), Ref: "pay-1"}); err != nil {
		t.Fatal(err)
	}
	if got := heard(2); len(got) != 2 || got[1] != "acme" {
		t.Fatalf("the replay told %v, want acme again", got)
	}
	if _, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme", Amount: money.FromCents(0)}); err == nil {
		t.Fatal("a deposit of nothing was accepted")
	}
	if _, err := f.Deposit(ctx, money.DepositInput{Org: "acme", Subject: "acme/carol", Amount: money.FromCents(700), Ref: "pay-1"}); err == nil {
		t.Fatal("a ref posted for another payment was accepted")
	}
	// A grant with no ref stacks, and tells.
	if _, err := f.Deposit(ctx, money.DepositInput{Org: "globex", Subject: "globex", Amount: money.FromCents(100)}); err != nil {
		t.Fatal(err)
	}
	if got := heard(3); len(got) != 3 || got[2] != "globex" {
		t.Fatalf("the deposits told %v, want acme's payment and its replay and globex's grant, and nothing for a refusal", got)
	}
	time.Sleep(50 * time.Millisecond)
	if got := heard(4); len(got) != 3 {
		t.Fatalf("told %v", got)
	}
}

// shaky is the books of an org whose first acknowledgement fails (the writer moved): the
// deposit is in the ledger and the caller is handed an error, so the payment processor
// retries.
type shaky struct {
	Ledgers
	failed atomic.Bool
}

func (s *shaky) Ship(org string, sandbox bool) (bool, error) {
	if !s.failed.Swap(true) {
		return false, errors.New("not durable yet")
	}
	return s.Ledgers.Ship(org, sandbox)
}

// A deposit whose first call posted the money and failed to settle it is retried by the
// payment processor; the retry is a replay, and it is the only call that gets as far as
// the money being in the wallet and acknowledged. It tells, so the runs refused for want
// of that money are tried.
//
// MUTATION: tell only on the call that posted (fresh) and the retry tells nothing: the
// money is in the wallet and no parked run is ever tried for it.
func TestADepositRetriedAfterAFailedSettleTells(t *testing.T) {
	ctx := context.Background()
	f := New(&shaky{Ledgers: Local(t.TempDir(), testMaster)})
	defer func() { _ = f.Close() }()
	var mu sync.Mutex
	told := 0
	SetDepositHook(func(string) { mu.Lock(); told++; mu.Unlock() })
	t.Cleanup(func() { SetDepositHook(nil) })
	in := money.DepositInput{Org: "acme", Subject: "acme/bob", Amount: money.FromCents(500), Ref: "pay-9"}
	if _, err := f.Deposit(ctx, in); err == nil {
		t.Fatal("the first call should have failed to settle")
	}
	time.Sleep(100 * time.Millisecond) // the hook is told on a goroutine of its own
	mu.Lock()
	early := told
	mu.Unlock()
	if early != 0 {
		t.Fatalf("a deposit that could not settle told %d times: the money is not acknowledged", early)
	}
	if _, err := f.Deposit(ctx, in); err != nil {
		t.Fatal(err)
	}
	waitUntil := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		n := told
		mu.Unlock()
		if n == 1 {
			return
		}
		if time.Now().After(waitUntil) {
			t.Fatalf("the money is in the wallet (posted by call 1, acknowledged by call 2) and the hook was told %d times, want once", n)
		}
		time.Sleep(time.Millisecond)
	}
}
