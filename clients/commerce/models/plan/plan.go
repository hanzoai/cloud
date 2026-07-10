package plan

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/types/refs"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var IgnoreFieldMismatch = datastore.IgnoreFieldMismatch

func init() { orm.Register[Plan]("plan") }

// Based On Stripe Plan
// Stripe\Plan JSON: {
//   "id": "gold21323",
//   "object": "plan",
//   "amount": 2000,
//   "created": 1386247539,
//   "currency": "usd",
//   "interval": "month",
//   "interval_count": 1,
//   "livemode": false,
//   "metadata": {
//   },
//   "name": "New plan name",
//   "statement_descriptor": null,
//   "trial_period_days": null
// }

type Plan struct {
	mixin.Model[Plan]

	// Unique human readable id
	Slug string `json:"slug"`
	// Internal id
	SKU string `json:"sku"`

	// Human readable name
	Name        string `json:"name"`
	Description string `json:"description"`

	Price           currency.Cents `json:"price"`
	Currency        currency.Type  `json:"currency"`
	Interval        Interval       `json:"interval"`
	IntervalCount   int            `json:"intervalCount"`
	TrialPeriodDays int            `json:"trialPeriodDays"`

	Metadata  Map    `json:"metadata" datastore:"-"`
	Metadata_ string `json:"-" datastore:"-"`

	Ref refs.EcommerceRef `json:"ref,omitempty"`
}

func (p *Plan) Load(ps []datastore.Property) (err error) {
	// Load supported properties
	if err = datastore.LoadStruct(p, ps); err != nil {
		return err
	}

	// Deserialize from datastore
	if len(p.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(p.Metadata_), &p.Metadata)
	}

	return err
}

func (p *Plan) Save() (ps []datastore.Property, err error) {
	// Serialize unsupported properties
	p.Metadata_ = string(json.EncodeBytes(&p.Metadata))

	if err != nil {
		return nil, err
	}

	// Save properties
	return datastore.SaveStruct(p)
}

func (p *Plan) Validator() *val.Validator {
	return val.New()
}

func New(db *datastore.Datastore) *Plan {
	p := new(Plan)
	p.Init(db)
	return p
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("plan")
}
