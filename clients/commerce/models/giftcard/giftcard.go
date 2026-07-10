// Package giftcard is the gift-card domain: a prepaid, code-addressable
// balance an org issues and a customer redeems against orders.
//
// Money-correctness design (READ THIS before touching redeem):
//
//   - A GiftCard carries an immutable InitialBalanceCents. It NEVER carries a
//     mutable "remaining" counter that two concurrent redeems could race on.
//   - Every debit is a GiftCardRedemption ledger line (append-only fact).
//   - The spendable balance is a PROJECTION: InitialBalance − Σ(active
//     redemptions). Because balance is derived, there is no lost-update: two
//     concurrent redeems each write their own ledger line and the projection
//     reflects both. (Rich Hickey: the redemptions are the values; balance is
//     a view.)
//   - Redemption is idempotent by construction: the ledger line's storage id
//     is deterministic in the caller-supplied idempotency key
//     (giftcardredemption.DeterministicID). A duplicate submit resolves to the
//     same row via the storage ON CONFLICT(id, kind, namespace) upsert — one
//     debit, never two — WITHOUT relying on datastore.RunInTransaction, which
//     is a no-op on this backend (datastore/datastore.go) and provides no
//     isolation.
//
// Over-redeem is prevented at apply time by checking the projected balance
// (Σ ledger) against the requested amount before writing the new line; the
// deterministic id makes the check-then-write safe against retries, and the
// projection makes distinct concurrent redeems additive rather than lossy.
package giftcard

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[GiftCard]("gift-card") }

// GiftCard is a prepaid balance addressable by Code, scoped to the issuing
// org's namespace. InitialBalanceCents is immutable after issue; the spendable
// balance is computed from the redemption ledger (see BalanceCents in the
// giftcard API), so a gift card row is a fact, not a mutable wallet.
type GiftCard struct {
	mixin.Model[GiftCard]

	// Code is the human-facing gift-card code (e.g. "GIFT-4F2A-9C1D").
	// Unique within an org namespace by convention (issue rejects collisions).
	// Stored uppercased for case-insensitive lookup.
	Code string `json:"code"`

	InitialBalanceCents currency.Cents `json:"initialBalanceCents"`
	Currency            currency.Type  `json:"currency" orm:"default:usd"`

	// Region this card is valid in (optional; empty = any region in the org).
	RegionId string `json:"regionId,omitempty"`

	// Optional order this card was purchased on (audit).
	OrderId string `json:"orderId,omitempty"`

	// Disabled cards cannot be redeemed even if balance remains.
	Disabled bool `json:"disabled"`

	// EndsAt, when set and in the past, makes the card unredeemable (expiry).
	EndsAt *time.Time `json:"endsAt,omitempty"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (g *GiftCard) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(g, ps); err != nil {
		return err
	}
	if len(g.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(g.Metadata_), &g.Metadata)
	}
	return err
}

func (g *GiftCard) Save() ([]datastore.Property, error) {
	g.Metadata_ = string(json.EncodeBytes(&g.Metadata))
	return datastore.SaveStruct(g)
}

// Redeemable reports whether the card may be debited right now, ignoring
// balance (balance is checked separately against the projected ledger sum).
func (g *GiftCard) Redeemable() bool {
	if g.Disabled {
		return false
	}
	if g.EndsAt != nil && !g.EndsAt.IsZero() && time.Now().After(*g.EndsAt) {
		return false
	}
	return true
}

func New(db *datastore.Datastore) *GiftCard {
	g := new(GiftCard)
	g.Init(db)
	return g
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("gift-card")
}
