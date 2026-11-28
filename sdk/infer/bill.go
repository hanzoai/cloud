package infer

import (
	"context"
	"sync"
)

// Bill is what one inference cost, in nano-USD, as the model API priced it: the
// amount the same answer debits any caller of api.hanzo.ai. The transport that
// carried the call tells it; the meter charges it. There is no second price for a
// call a program in this process makes — no rate of its own, no estimate standing
// in for the answer's price.
//
// It is told, never guessed: a call whose transport reported nothing leaves it
// untold, and the meter can tell that apart from a call that cost nothing.
type Bill struct {
	mu      sync.Mutex
	nanos   int64
	told    bool
	covered bool
	cash    bool
}

type billKey struct{}

// WithBill is ctx carrying a fresh Bill for the one call about to be made on it.
func WithBill(ctx context.Context) (context.Context, *Bill) {
	b := &Bill{}
	return context.WithValue(ctx, billKey{}, b), b
}

// BillOf is the Bill ctx carries, nil when the caller asked for none.
func BillOf(ctx context.Context) *Bill {
	b, _ := ctx.Value(billKey{}).(*Bill)
	return b
}

// Tell adds what one answer the call bought cost. A nil Bill is told nothing.
func (b *Bill) Tell(nanos int64) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.nanos += nanos
	b.told = true
	b.mu.Unlock()
}

// Nanos is the call's price, and whether anything told one.
func (b *Bill) Nanos() (int64, bool) {
	if b == nil {
		return 0, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nanos, b.told
}

// Cover marks the call as paid by the caller's plan or a free cap, so the meter
// releases its hold without debiting the wallet or logging an unpriced call.
func (b *Bill) Cover() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.covered = true
	b.mu.Unlock()
}

// Covered reports whether the call was paid by the caller's plan or a free cap.
func (b *Bill) Covered() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.covered
}

// SetCash marks the billed call as prepaid-cash-only (never drawn from granted
// credit).
func (b *Bill) SetCash() {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.cash = true
	b.mu.Unlock()
}

// Cash reports whether only prepaid cash may pay for the billed call.
func (b *Bill) Cash() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.cash
}
