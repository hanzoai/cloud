package giftcard

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/giftcardredemption"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
)

// TestRedeem_NoOverdraft_DistinctKeysConcurrent is the sharpest money attack:
// many concurrent redeems with DIFFERENT idempotency keys, each for the full
// card value, on a small-balance card. Without per-card serialization they all
// read the same pre-state, all pass the balance check, and all debit — draining
// the card past its initial balance (negative balance = money created). The
// per-card lock must let exactly ONE full-value redeem through; the rest get
// ErrInsufficientFunds. Balance must never go negative.
func TestRedeem_NoOverdraft_DistinctKeysConcurrent(t *testing.T) {
	c := ae.NewContext()
	defer c.Close()
	db := datastore.New(nscontext.WithNamespace(context.Background(), "acme"))

	g := New(db)
	g.Code = "OVERDRAFT"
	g.InitialBalanceCents = 1000 // $10
	g.Currency = "usd"
	if err := g.Create(); err != nil {
		t.Fatalf("issue: %v", err)
	}

	const n = 12
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			gdb := datastore.New(nscontext.WithNamespace(context.Background(), "acme"))
			gc := New(gdb)
			_ = gc.GetById(g.Id())
			// Each request tries to draw the FULL $10 with a unique key.
			_, _, _ = Redeem(gdb, gc, 1000, "usd", "", fmt.Sprintf("key_%d", i))
		}(i)
	}
	wg.Wait()

	// Exactly one $10 debit should have committed.
	lines := make([]*giftcardredemption.GiftCardRedemption, 0, n)
	if _, err := giftcardredemption.Query(db).Filter("GiftCardId=", g.Id()).GetAll(&lines); err != nil {
		t.Fatalf("query ledger: %v", err)
	}
	var total currency.Cents
	for _, l := range lines {
		total += l.EffectiveAmount()
	}
	if total > g.InitialBalanceCents {
		t.Fatalf("OVERDRAFT: committed %d cents on a %d-cent card (%d lines)", total, g.InitialBalanceCents, len(lines))
	}

	bal, err := BalanceCents(db, g)
	if err != nil {
		t.Fatalf("balance: %v", err)
	}
	if bal < 0 {
		t.Fatalf("OVERDRAFT: balance = %d < 0", bal)
	}
	// Exactly one full-value redemption fits a $10 card.
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 committed redemption (balance permits one), got %d", len(lines))
	}
	if bal != 0 {
		t.Fatalf("balance after the single $10 redeem = %d, want 0", bal)
	}
}
