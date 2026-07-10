// Package giftcardredemption is the append-only debit ledger for gift cards.
//
// Each GiftCardRedemption is one immutable fact: "amount X was drawn from gift
// card G under idempotency key K". The gift card's spendable balance is the
// projection InitialBalance − Σ(active redemptions) — there is no mutable
// balance counter, so concurrent distinct redemptions are additive (never
// lost).
//
// Idempotency is enforced at the storage layer, NOT via
// datastore.RunInTransaction (which is a no-op on this backend and gives no
// isolation). The model registers WithStringKey and its id is set to
// DeterministicID(giftCardId, idempotencyKey) before Create. Two concurrent
// creates with the same key therefore resolve to the SAME row via the storage
// ON CONFLICT(id, kind, namespace) upsert — a duplicate submit debits once,
// never twice.
package giftcardredemption

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/orm"
)

func init() {
	orm.Register[GiftCardRedemption]("gift-card-redemption", orm.WithStringKey[GiftCardRedemption]())
}

// GiftCardRedemption records a single debit (or, for AmountCents < 0, a
// credit/void reversal) against a gift card. Append-only: never mutate an
// existing line's amount; reverse it with a compensating line instead.
type GiftCardRedemption struct {
	mixin.Model[GiftCardRedemption]

	GiftCardId  string         `json:"giftCardId"`
	AmountCents currency.Cents `json:"amountCents"`
	Currency    currency.Type  `json:"currency" orm:"default:usd"`

	// OrderId the redemption was applied to (audit; optional for manual debits).
	OrderId string `json:"orderId,omitempty"`

	// IdempotencyKey is the caller-supplied dedup key. The row id is derived
	// from (GiftCardId, IdempotencyKey), so re-submitting the same key is a
	// no-op replay rather than a second debit.
	IdempotencyKey string `json:"idempotencyKey"`

	// IsReversal marks this line as a compensating (negative-amount) entry that
	// cancels an earlier debit. It is informational for audit/UX; the math is
	// carried entirely by AmountCents (a reversal is simply a negative amount),
	// so the balance projection never special-cases it.
	IsReversal bool `json:"isReversal,omitempty"`

	// ReversesId, on a reversal line, is the id of the redemption it cancels.
	ReversesId string `json:"reversesId,omitempty"`
}

func (r *GiftCardRedemption) Load(ps []datastore.Property) error {
	return datastore.LoadStruct(r, ps)
}

func (r *GiftCardRedemption) Save() ([]datastore.Property, error) {
	return datastore.SaveStruct(r)
}

// DeterministicID derives the stable storage id for a (giftCardId,
// idempotencyKey) pair. Same inputs → same id → ON CONFLICT dedup at the
// storage layer. Prefixed so the id is human-recognizable in logs/audits.
func DeterministicID(giftCardId, idempotencyKey string) string {
	sum := sha256.Sum256([]byte(giftCardId + "\x00" + idempotencyKey))
	return "gcr_" + hex.EncodeToString(sum[:16])
}

// EffectiveAmount is the amount this line contributes to the drawn total. A
// reversal is simply a negative amount, so summing AmountCents across all lines
// yields the net drawn total — no special-casing.
func (r *GiftCardRedemption) EffectiveAmount() currency.Cents {
	return r.AmountCents
}

func New(db *datastore.Datastore) *GiftCardRedemption {
	r := new(GiftCardRedemption)
	r.Init(db)
	return r
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("gift-card-redemption")
}
