package shippingoptionrule

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[ShippingOptionRule]("shippingoptionrule") }

type ShippingOptionRule struct {
	mixin.Model[ShippingOptionRule]

	Attribute        string `json:"attribute"`
	Operator         string `json:"operator"` // "eq", "ne", "gt", "lt", "gte", "lte", "in", "nin"
	Value            string `json:"value" datastore:",noindex"`
	ShippingOptionId string `json:"shippingOptionId"`
}

func New(db *datastore.Datastore) *ShippingOptionRule {
	r := new(ShippingOptionRule)
	r.Init(db)
	return r
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("shippingoptionrule")
}
