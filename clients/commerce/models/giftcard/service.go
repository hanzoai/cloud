package giftcard

import (
	"errors"
	"strings"
	"sync"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/giftcardredemption"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
)

// redeemLocks serializes the balance-check-then-debit critical section PER gift
// card (keyed by namespace+cardId), closing the concurrent-overdraft window:
// two redeems with DIFFERENT idempotency keys must not both pass the balance
// check against the same pre-state and both debit past the initial balance.
//
// This is correct for the deployment model — commerce runs single-pod
// (k8s replicas:1 + Recreate, ReadWriteOnce SQLite PVC), so one process owns
// the store and an in-process lock is the serialization point. The SAME-key
// case is additionally closed by the deterministic-id ON CONFLICT upsert, and
// cross-card redeems never contend (distinct keys). If commerce ever runs
// multi-writer, this must move to a real DB transaction
// (datastore.DB().RunInTransaction, which takes the SQLite write lock).
var redeemLocks sync.Map // map[string]*sync.Mutex

func lockFor(key string) *sync.Mutex {
	m, _ := redeemLocks.LoadOrStore(key, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// Money errors. Callers map these to 4xx; none of them move money.
var (
	ErrNotRedeemable      = errors.New("gift card is not redeemable (disabled or expired)")
	ErrInsufficientFunds  = errors.New("gift card has insufficient balance")
	ErrNonPositiveAmount  = errors.New("redeem amount must be positive")
	ErrMissingIdempotency = errors.New("idempotency key is required to redeem")
	ErrCurrencyMismatch   = errors.New("redeem currency does not match gift card currency")
)

// DrawnCents returns the total non-voided amount already redeemed from a card.
// It is the Σ over the append-only redemption ledger — the authoritative debit
// total. Balance is derived from it, never stored, so concurrent distinct
// redemptions are additive (never lost).
func DrawnCents(db *datastore.Datastore, giftCardId string) (currency.Cents, error) {
	lines := make([]*giftcardredemption.GiftCardRedemption, 0, 8)
	if _, err := giftcardredemption.Query(db).
		Filter("GiftCardId=", giftCardId).
		GetAll(&lines); err != nil {
		return 0, err
	}
	var drawn currency.Cents
	for _, l := range lines {
		drawn += l.EffectiveAmount()
	}
	return drawn, nil
}

// BalanceCents is the spendable balance: InitialBalance − Σ(active redemptions).
// This is a projection over facts; there is no mutable balance field to race.
func BalanceCents(db *datastore.Datastore, g *GiftCard) (currency.Cents, error) {
	drawn, err := DrawnCents(db, g.Id())
	if err != nil {
		return 0, err
	}
	return g.InitialBalanceCents - drawn, nil
}

// Redeem draws amount from the gift card and returns the resulting redemption
// line plus the balance remaining AFTER this redemption.
//
// Idempotency (money-critical): the redemption row id is deterministic in
// idempotencyKey (giftcardredemption.DeterministicID). A retry with the same
// key resolves to the same stored row via the storage ON CONFLICT upsert —
// the amount is debited exactly once, never twice, WITHOUT relying on
// datastore.RunInTransaction (a no-op that gives no isolation on this backend).
//
// On a duplicate key we return the ALREADY-STORED line (not the caller's
// possibly-different amount) so a replayed request can never silently change a
// past debit.
//
// db MUST be namespaced to the gift card's org (the caller passes
// org.Namespaced(c)); every read/write below is thereby tenant-scoped.
func Redeem(db *datastore.Datastore, g *GiftCard, amount currency.Cents, curr currency.Type, orderId, idempotencyKey string) (*giftcardredemption.GiftCardRedemption, currency.Cents, error) {
	if amount <= 0 {
		return nil, 0, ErrNonPositiveAmount
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return nil, 0, ErrMissingIdempotency
	}
	if curr != "" && curr != g.Currency {
		return nil, 0, ErrCurrencyMismatch
	}
	if !g.Redeemable() {
		return nil, 0, ErrNotRedeemable
	}

	id := giftcardredemption.DeterministicID(g.Id(), idempotencyKey)

	// Serialize the check-then-debit for THIS card so distinct-key concurrent
	// redeems can't both pass the balance check against the same pre-state and
	// overdraw. Keyed by namespace+card so other cards/tenants never contend.
	mu := lockFor(nscontext.GetNamespace(db.Context) + "\x00" + g.Id())
	mu.Lock()
	defer mu.Unlock()

	// Idempotent replay: if this exact (card, key) was already redeemed, return
	// the stored line unchanged — do NOT debit again, and do NOT honor a
	// different amount on the replay.
	existing := giftcardredemption.New(db)
	if err := existing.GetById(id); err == nil {
		bal, berr := BalanceCents(db, g)
		if berr != nil {
			return nil, 0, berr
		}
		return existing, bal, nil
	} else if !errors.Is(err, datastore.ErrNoSuchEntity) {
		return nil, 0, err
	}

	// First time for this key: check the projected balance covers the draw.
	// Safe under the per-card lock — no other redeem for this card can interleave
	// between this read and the write below.
	drawn, err := DrawnCents(db, g.Id())
	if err != nil {
		return nil, 0, err
	}
	if g.InitialBalanceCents-drawn < amount {
		return nil, 0, ErrInsufficientFunds
	}

	line := giftcardredemption.New(db)
	line.SetId(id) // deterministic id ⇒ ON CONFLICT dedup at storage (same-key safety)
	line.GiftCardId = g.Id()
	line.AmountCents = amount
	line.Currency = g.Currency
	line.OrderId = orderId
	line.IdempotencyKey = idempotencyKey
	if err := line.Create(); err != nil {
		return nil, 0, err
	}

	// Recompute balance from the ledger (includes the line we just wrote, and
	// any concurrent distinct-key line that landed in between).
	bal, err := BalanceCents(db, g)
	if err != nil {
		return nil, 0, err
	}
	return line, bal, nil
}

// Void reverses a redemption by APPENDING a compensating reversal line (a
// negative-amount ledger entry) rather than mutating the original — the ledger
// is append-only, so a debit is never rewritten; it is cancelled by an equal
// and opposite fact. This restores the drawn amount to the projected balance.
//
// Idempotent: the reversal's id is deterministic in the original redemption id
// ("void:"+id), so voiding the same redemption twice resolves to the same
// reversal row via ON CONFLICT — the balance is restored exactly once. Returns
// the balance after the void.
func Void(db *datastore.Datastore, g *GiftCard, redemptionId string) (currency.Cents, error) {
	orig := giftcardredemption.New(db)
	if err := orig.GetById(redemptionId); err != nil {
		return 0, err
	}
	// Already reversed? The reversal line's presence is the idempotency guard.
	reversalKey := "void:" + redemptionId
	reversalID := giftcardredemption.DeterministicID(g.Id(), reversalKey)

	existing := giftcardredemption.New(db)
	if err := existing.GetById(reversalID); err == nil {
		return BalanceCents(db, g) // already voided — no-op
	} else if !errors.Is(err, datastore.ErrNoSuchEntity) {
		return 0, err
	}

	rev := giftcardredemption.New(db)
	rev.SetId(reversalID)
	rev.GiftCardId = g.Id()
	rev.AmountCents = -orig.AmountCents // compensating credit
	rev.Currency = orig.Currency
	rev.OrderId = orig.OrderId
	rev.IdempotencyKey = reversalKey
	rev.IsReversal = true
	rev.ReversesId = redemptionId
	if err := rev.Create(); err != nil {
		return 0, err
	}
	return BalanceCents(db, g)
}
