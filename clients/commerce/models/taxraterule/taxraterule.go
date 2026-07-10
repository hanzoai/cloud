package taxraterule

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[TaxRateRule]("taxraterule") }

type TaxRateRule struct {
	mixin.Model[TaxRateRule]

	TaxRateId   string `json:"taxRateId"`
	Reference   string `json:"reference"`
	ReferenceId string `json:"referenceId"`
}

func New(db *datastore.Datastore) *TaxRateRule {
	t := new(TaxRateRule)
	t.Init(db)
	return t
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("taxraterule")
}
