// Copyright © 2026 Hanzo AI. MIT License.

package checkout

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// TestRefund_OverRefund_RaceClean is the -race proof for HIGH-3. It exercises
// the EXACT concurrency control the Refund handler uses — the real
// refundLockFor(order) mutex wrapping a reload → over-refund guard → commit on
// ord.Refunded — from many goroutines at once, with NO logging or o11y in the
// critical section. It asserts the money invariant (exactly one 3000 refund
// commits against a 5000 order; Refunded never exceeds the captured total) AND,
// under `go test -race`, that the check-then-write is data-race-clean.
//
// This is the companion to TestRefund_Concurrent_DistinctAmounts_NoOverRefund
// (the full-handler functional regression guard, //go:build !race): together
// they prove the fix is both correctly wired AND race-clean, while keeping the
// race proof free of the pre-existing shared-logger/async-counter infra races
// the full handler drags in.
func TestRefund_OverRefund_RaceClean(t *testing.T) {
	tc := ae.NewContext()
	defer tc.Close()

	const ns = "acme"
	base := nscontext.WithNamespace(context.Background(), ns)
	db := datastore.New(base)

	ord := order.New(db)
	ord.Total = 5000
	ord.Paid = 5000
	if err := ord.Create(); err != nil {
		t.Fatalf("seed order: %v", err)
	}
	orderId := ord.Id()

	// The refund's check-then-write on ord.Refunded, guarded by the SAME
	// per-order primitive the handler uses. Mirrors handlers.go: lock →
	// reload-in-lock → square.Refund's over-refund guard → commit.
	lockKey := ns + "\x00" + orderId
	var committed int64
	attempt := func(amount currency.Cents) {
		mu := refundLockFor(lockKey)
		mu.Lock()
		defer mu.Unlock()

		fresh := order.New(db)
		if err := fresh.GetById(orderId); err != nil {
			return
		}
		// Over-refund guard (square/refund.go:46): reject if this refund would
		// push total refunded past the captured total.
		if fresh.Refunded+amount > fresh.Total {
			return
		}
		fresh.Refunded += amount
		fresh.Paid -= amount
		if err := fresh.Put(); err != nil {
			t.Errorf("commit refund: %v", err)
			return
		}
		atomic.AddInt64(&committed, 1)
	}

	const goroutines = 8
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() { defer wg.Done(); attempt(3000) }()
	}
	wg.Wait()

	if got := atomic.LoadInt64(&committed); got != 1 {
		t.Fatalf("%d refunds committed — want exactly 1 (over-refund race not prevented)", got)
	}

	final := order.New(db)
	if err := final.GetById(orderId); err != nil {
		t.Fatalf("reload order: %v", err)
	}
	if int64(final.Refunded) > 5000 {
		t.Fatalf("OVER-REFUND: order Refunded=%d exceeds captured total 5000", final.Refunded)
	}
	if int64(final.Refunded) != 3000 {
		t.Fatalf("Refunded=%d, want exactly one 3000 refund", final.Refunded)
	}
}
