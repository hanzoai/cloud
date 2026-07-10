package subscription

import (
	"errors"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/deprecated/plan"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/accounts"
	"github.com/hanzoai/cloud/clients/commerce/models/types/refs"
	"github.com/hanzoai/cloud/clients/commerce/util/hashid"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/timeutil"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	. "github.com/hanzoai/cloud/clients/commerce/types"
)

// Registration handled by models/subscription

// Based On Stripe Subscription
// Stripe\Subscription JSON: {
//   "id": "sub_7OTicGsP51uH9F",
//   "object": "subscription",
//   "application_fee_percent": null,
//   "cancel_at_period_end": false,
//   "canceled_at": null,
//   "current_period_end": 1450725048,
//   "current_period_start": 1448133048,
//   "customer": "cus_7OSfdiUiYYf0tS",
//   "discount": null,
//   "ended_at": null,
//   "metadata": {
//   },
//   "plan": {
//		...
//   },
//   "quantity": 1,
//   "start": 1448133048,
//   "status": "active",
//   "tax_percent": null,
//   "trial_end": null,
//   "trial_start": null
// }

var IgnoreFieldMismatch = datastore.IgnoreFieldMismatch

type BillingType string

const (
	Charge  BillingType = "charge_automatically"
	Invoice BillingType = "send_invoice"
)

type Status string

const (
	Trialing  Status = "trialing"
	Active    Status = "active"
	PastDue   Status = "past_due"
	Cancelled Status = "canceled"
	Unpaid    Status = "unpaid"
)

type Subscription struct {
	mixin.Model[Subscription]

	Number int `json:"number,omitempty" datastore:"-"`

	// Immutable buyer data from time of payment, may or may not be associated
	// with a user.
	Buyer Buyer `json:"buyer"`

	Type BillingType `json:"billing_type"`

	PlanId string `json:"planId"`
	UserId string `json:"userId"`

	FeePercent float64 `json:"application_fee_percent"`
	EndCancel  bool    `json:"cancel_at_period_end"`

	PeriodStart time.Time `json:"current_period_start"`
	PeriodEnd   time.Time `json:"current_period_end"`

	Start      time.Time `json:"start"`
	Ended      time.Time `json:"ended_at"`
	Canceled   bool      `json:"canceled"`
	CanceledAt time.Time `json:"canceled_at"`

	TrialStart time.Time `json:"trial_start"`
	TrialEnd   time.Time `json:"trial_end"`

	Plan     plan.Plan `json:"plan"`
	Quantity int       `json:"quantity"`
	Status   Status    `json:"status"`

	Metadata  Map    `json:"metadata" datastore:"-"`
	Metadata_ string `json:"-" datastore:"-"`

	// Stripe livemode
	Live bool `json:"live"`

	// Internal testing flag
	Test bool `json:"-"`

	Account accounts.Account  `json:"account,omitempty"`
	Ref     refs.EcommerceRef `json:"ref,omitempty"`
}

func (s *Subscription) Load(ps []datastore.Property) (err error) {
	// Ensure we're initialized

	// Load supported properties
	if err = IgnoreFieldMismatch(datastore.LoadStruct(s, ps)); err != nil {
		return err
	}

	// Set order number
	num, err := s.NumberFromId()
	if err != nil {
		return err
	}
	s.Number = num

	// Deserialize from datastore
	if len(s.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(s.Metadata_), &s.Metadata)
	}

	return err
}

func (s *Subscription) Save() (ps []datastore.Property, err error) {
	// Serialize unsupported properties
	s.Metadata_ = string(json.EncodeBytes(&s.Metadata))

	if err != nil {
		return nil, err
	}

	// Save properties
	return datastore.SaveStruct(s)
}

func (s *Subscription) Validator() *val.Validator {
	return val.New()
}

func (s Subscription) NumberFromId() (i int, err error) {
	if s.Id_ == "" {
		return -1, errors.New("Subscription.NumberFromID(): Blank ID passed.")
	}

	ret, err := hashid.Decode(s.Id_)

	return ret[1], err
}

func (s Subscription) TrialPeriodsRemaining() int {
	years, months := timeutil.YearMonthDiff(s.TrialStart, s.TrialEnd)

	if s.Plan.Interval == Monthly {
		return months
	}
	return years
}

func (s Subscription) PeriodsRemaining() int {
	months, years := timeutil.YearMonthDiff(s.PeriodStart, s.PeriodEnd)

	if s.Plan.Interval == Monthly {
		return months
	}
	return years
}

func New(db *datastore.Datastore) *Subscription {
	s := new(Subscription)
	s.Init(db)
	s.Metadata = make(map[string]interface{})
	return s
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("subscription")
}
