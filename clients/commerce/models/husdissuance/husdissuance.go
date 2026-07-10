// Package husdissuance is the durable, idempotent record of ONE treasury HUSD
// mint in the chain-backed credit ledger.
//
// Each record is keyed by a DETERMINISTIC storage id derived from the mint's
// idempotency key (treasury.IssuanceID), so a duplicate mint request collapses
// onto the same row via the backend ON CONFLICT(id, kind, namespace) upsert —
// the same money-safe primitive as models/idempotencykey and gift-card
// redemptions. The record is the audit anchor: every credit that exists traces
// to exactly one issuance and its on-chain TxHash. It carries no key material.
package husdissuance

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/orm"
)

func init() {
	orm.Register[HUSDIssuance]("husd-issuance", orm.WithStringKey[HUSDIssuance]())
}

// HUSDIssuance mirrors treasury.Issuance for storage. Bucket/Status are plain
// strings here (the typed enums live in package treasury); the datastore adapter
// converts at the boundary. There is deliberately NO field named Key (it would
// shadow the embedded Model[T].Key() method — see entity_guard.go).
type HUSDIssuance struct {
	mixin.Model[HUSDIssuance]

	// IdemKey is the caller idempotency key; the storage id is derived from it, so
	// re-submitting the same key is a replay, never a second mint.
	IdemKey string `json:"idemKey"`
	OrgID   string `json:"orgId"`
	// Subject is the off-chain ledger DestinationId this mint credits (indexed so
	// the reconcile job can group issuances per subject as well as per org).
	Subject     string `json:"subject"`
	OrgAddress  string `json:"orgAddress"`
	AmountCents int64  `json:"amountCents"`
	Bucket      string `json:"bucket"`
	Reason      string `json:"reason,omitempty"`
	// Test marks a test-chain mint (matches the ledger Test partition).
	Test      bool   `json:"test"`
	ChainID   int64  `json:"chainId"`
	TokenAddr string `json:"tokenAddr"`
	// TxHash is INDEXED so the indexer's IssuanceLookup.ByTxHash resolves a
	// projected on-chain transfer back to its off-chain bucket/subject.
	TxHash   string    `json:"txHash,omitempty"`
	Status   string    `json:"status" orm:"default:pending"`
	MintedAt time.Time `json:"mintedAt,omitempty"`
}

func (i *HUSDIssuance) Load(ps []datastore.Property) error {
	return datastore.LoadStruct(i, ps)
}

func (i *HUSDIssuance) Save() ([]datastore.Property, error) {
	return datastore.SaveStruct(i)
}

// New returns an initialized HUSDIssuance bound to db.
func New(db *datastore.Datastore) *HUSDIssuance {
	i := new(HUSDIssuance)
	i.Init(db)
	return i
}

// Query returns a datastore query over the husd-issuance kind (audit/rollup).
func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("husd-issuance")
}
