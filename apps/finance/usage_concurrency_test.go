package finance

import (
	"context"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"
)

// EXACTLY-ONCE UNDER CONTENTION, on the wallet-scoped key the backfill repairs to.
//
// The probe and the insert have to be one atomic decision or the key buys nothing: sixteen
// callers racing on ONE act must produce ONE debit, and sixteen racing on SIXTEEN acts must
// produce sixteen. Both are asserted together because a lock that made the first true by
// serializing everything could still let the second lose a write.
//
// Run with -race: the answer comes from inside the same transaction as the insert
// (RecordUsageOnce), so two concurrent replays cannot both read posted=true.
func TestUsageIsExactlyOnceUnderConcurrency(t *testing.T) {
	ctx := context.Background()
	f := New(t.TempDir())
	defer func() { _ = f.Close() }()

	if _, err := f.Deposit(ctx, types.DepositInput{
		Org: "acme", Subject: "acme", Amount: money.FromCents(10_000),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	const racers = 16

	// ONE act, sixteen ways: exactly one of them may move money.
	var posted int64
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range racers {
		wg.Go(func() {
			id, ok, err := f.RecordUsageOnce(ctx, types.UsageInput{
				Org: "acme", Subject: "acme", Amount: money.FromCents(100), Ref: "act_one",
			})
			if err != nil {
				t.Errorf("record: %v", err)
				return
			}
			if id == "" {
				t.Error("no entry id for a posted act")
			}
			if ok {
				mu.Lock()
				posted++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if posted != 1 {
		t.Fatalf("%d of %d racers posted ONE act; want exactly 1", posted, racers)
	}
	mustBalance(t, f, "acme", "acme", 10_000-100)

	// Sixteen DIFFERENT acts, raced: every one bills, none is lost to the other's key.
	for i := range racers {
		wg.Go(func() {
			if err := f.RecordUsage(ctx, types.UsageInput{
				Org: "acme", Subject: "acme", Amount: money.FromCents(10), Ref: refOf(i),
			}); err != nil {
				t.Errorf("distinct act %d: %v", i, err)
			}
		})
	}
	wg.Wait()
	mustBalance(t, f, "acme", "acme", 10_000-100-racers*10)

	// Sixteen racers on sixteen WALLETS under ONE ref: the key is scoped to the wallet, so
	// each of them is its own act. This is the property the program column carries and the
	// one the backfill restores for rows written before it existed.
	for i := range racers {
		wg.Go(func() {
			subject := "acme/" + refOf(i)
			if _, derr := f.Deposit(ctx, types.DepositInput{
				Org: "acme", Subject: subject, Amount: money.FromCents(100),
			}); derr != nil {
				t.Errorf("seed %s: %v", subject, derr)
				return
			}
			if err := f.RecordUsage(ctx, types.UsageInput{
				Org: "acme", Subject: subject, Amount: money.FromCents(7), Ref: "act_shared",
			}); err != nil {
				t.Errorf("per-wallet act %d: %v", i, err)
			}
		})
	}
	wg.Wait()
	for i := range racers {
		mustBalance(t, f, "acme", "acme/"+refOf(i), 93)
	}
}

func refOf(i int) string {
	const digits = "0123456789abcdef"
	return "act_" + string(digits[i%len(digits)])
}
