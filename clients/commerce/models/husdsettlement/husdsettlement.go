// Package husdsettlement is the audit record of ONE org→treasury HUSD settlement:
// the on-chain sweep that reconciles an org's on-chain balance back down to its
// off-chain ledger balance after metered usage (and reclaimed/expired grants)
// drew it down. It carries no key material; its TxHash is the on-chain anchor.
package husdsettlement

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/orm"
)

func init() {
	orm.Register[HUSDSettlement]("husd-settlement", orm.WithStringKey[HUSDSettlement]())
}

// HUSDSettlement records one settlement sweep for an org.
type HUSDSettlement struct {
	mixin.Model[HUSDSettlement]

	OrgID       string    `json:"orgId"`
	OrgAddress  string    `json:"orgAddress"`
	AmountCents int64     `json:"amountCents"`
	Test        bool      `json:"test"`
	ChainID     int64     `json:"chainId"`
	// TxHash is INDEXED (audit lookup) — the on-chain org→treasury transfer.
	TxHash    string    `json:"txHash"`
	SettledAt time.Time `json:"settledAt"`
}

func (s *HUSDSettlement) Load(ps []datastore.Property) error { return datastore.LoadStruct(s, ps) }
func (s *HUSDSettlement) Save() ([]datastore.Property, error) { return datastore.SaveStruct(s) }

// New returns an initialized HUSDSettlement bound to db.
func New(db *datastore.Datastore) *HUSDSettlement {
	s := new(HUSDSettlement)
	s.Init(db)
	return s
}

// Query returns a datastore query over the husd-settlement kind (audit/rollup).
func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("husd-settlement")
}
