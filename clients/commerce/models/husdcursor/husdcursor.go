// Package husdcursor persists the HUSD indexer's scan position: the last block
// fully projected into the ledger, per chain. It lets a restart resume rather
// than rescan from genesis. The record is a singleton per chain id (deterministic
// storage id), lives in the global "system" namespace, and carries no money — it
// is pure indexer bookkeeping.
package husdcursor

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/orm"
)

func init() {
	orm.Register[HUSDCursor]("husd-cursor", orm.WithStringKey[HUSDCursor]())
}

// HUSDCursor is the indexer's last fully-scanned block for one chain.
type HUSDCursor struct {
	mixin.Model[HUSDCursor]

	ChainID   int64  `json:"chainId"`
	LastBlock uint64 `json:"lastBlock"`
}

func (c *HUSDCursor) Load(ps []datastore.Property) error { return datastore.LoadStruct(c, ps) }
func (c *HUSDCursor) Save() ([]datastore.Property, error) { return datastore.SaveStruct(c) }

// New returns an initialized HUSDCursor bound to db.
func New(db *datastore.Datastore) *HUSDCursor {
	c := new(HUSDCursor)
	c.Init(db)
	return c
}
