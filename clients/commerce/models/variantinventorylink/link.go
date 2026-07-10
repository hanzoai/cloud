package variantinventorylink

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[VariantInventoryLink]("variantinventorylink") }

type VariantInventoryLink struct {
	mixin.Model[VariantInventoryLink]

	VariantId       string `json:"variantId"`
	InventoryItemId string `json:"inventoryItemId"`
}

func New(db *datastore.Datastore) *VariantInventoryLink {
	l := new(VariantInventoryLink)
	l.Init(db)
	return l
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("variantinventorylink")
}
