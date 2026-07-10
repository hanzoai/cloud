// Package employee is the B2B buyer domain: a person who purchases on behalf
// of a company (models/company). Each employee carries a spending limit; the
// pure spend arithmetic lives in spending.go so it can be unit-tested and
// reused by the approval workflow without a datastore.
package employee

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[Employee]("employee") }

// Employee links a customer (the user placing orders) to a company and caps
// how much that user may commit against the company. A zero SpendingLimitCents
// means unlimited (see spending.go).
type Employee struct {
	mixin.Model[Employee]

	CompanyId  string `json:"companyId"`
	CustomerId string `json:"customerId"`

	IsAdmin bool `json:"isAdmin"`

	// SpendingLimitCents caps committed spend for this employee. 0 = unlimited.
	SpendingLimitCents currency.Cents `json:"spendingLimitCents"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (e *Employee) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(e, ps); err != nil {
		return err
	}
	if len(e.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(e.Metadata_), &e.Metadata)
	}
	return err
}

func (e *Employee) Save() ([]datastore.Property, error) {
	e.Metadata_ = string(json.EncodeBytes(&e.Metadata))
	return datastore.SaveStruct(e)
}

func New(db *datastore.Datastore) *Employee {
	e := new(Employee)
	e.Init(db)
	return e
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("employee")
}
