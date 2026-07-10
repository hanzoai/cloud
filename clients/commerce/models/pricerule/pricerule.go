package pricerule

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[PriceRule]("pricerule") }

type PriceRule struct {
	mixin.Model[PriceRule]

	PriceId   string `json:"priceId"`
	Attribute string `json:"attribute"`
	Value     string `json:"value"`
	Operator  string `json:"operator"`
	Priority  int    `json:"priority"`
}

func (p *PriceRule) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(p, ps); err != nil {
		return err
	}

	return err
}

func (p *PriceRule) Save() ([]datastore.Property, error) {
	return datastore.SaveStruct(p)
}

func New(db *datastore.Datastore) *PriceRule {
	t := new(PriceRule)
	t.Init(db)
	return t
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("pricerule")
}
