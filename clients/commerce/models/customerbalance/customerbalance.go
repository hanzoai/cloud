package customerbalance

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[CustomerBalance]("customer-balance") }

// CustomerBalance tracks a customer's stored-value balance per currency.
// Positive balance = credit available to settle invoices.
type CustomerBalance struct {
	mixin.Model[CustomerBalance]

	CustomerId string        `json:"customerId"`
	Currency   currency.Type `json:"currency" orm:"default:usd"`
	Balance    int64         `json:"balance"` // cents, positive = credit
}

func (cb *CustomerBalance) Load(ps []datastore.Property) (err error) {
	return datastore.LoadStruct(cb, ps)
}

func (cb *CustomerBalance) Save() (ps []datastore.Property, err error) {
	return datastore.SaveStruct(cb)
}

func (cb *CustomerBalance) Validator() *val.Validator {
	return nil
}

func New(db *datastore.Datastore) *CustomerBalance {
	cb := new(CustomerBalance)
	cb.Init(db)
	cb.Parent = db.NewKey("synckey", "", 1, nil)
	return cb
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("customer-balance")
}
