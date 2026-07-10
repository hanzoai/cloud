// Package quote is the B2B request-for-quote domain: a company (models/company)
// negotiates the price of a draft order with the merchant. A quote threads
// messages (message.go) between customer and merchant until it is accepted or
// rejected by either side.
package quote

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[Quote]("quote") }

// Quote lifecycle states.
const (
	StatusPending          = "pending"
	StatusCustomerRejected = "customer_rejected"
	StatusMerchantRejected = "merchant_rejected"
	StatusAccepted         = "accepted"
)

// Quote is a B2B RFQ against a draft order. TotalCents is the negotiated total
// the merchant is offering; the quote is actionable (acceptable/rejectable)
// only while pending.
type Quote struct {
	mixin.Model[Quote]

	CompanyId  string `json:"companyId"`
	CustomerId string `json:"customerId"`

	// OrderId is the draft order this quote prices.
	OrderId string `json:"orderId"`

	Status       string         `json:"status" orm:"default:pending"`
	TotalCents   currency.Cents `json:"totalCents"`
	CurrencyCode currency.Type  `json:"currencyCode" orm:"default:usd"`

	ValidUntil *time.Time `json:"validUntil,omitempty"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (q *Quote) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(q, ps); err != nil {
		return err
	}
	if len(q.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(q.Metadata_), &q.Metadata)
	}
	return err
}

func (q *Quote) Save() ([]datastore.Property, error) {
	q.Metadata_ = string(json.EncodeBytes(&q.Metadata))
	return datastore.SaveStruct(q)
}

// IsActionable reports whether the quote may still be accepted or rejected.
// Only pending quotes are actionable.
func (q *Quote) IsActionable() bool {
	return q.Status == StatusPending
}

func New(db *datastore.Datastore) *Quote {
	q := new(Quote)
	q.Init(db)
	return q
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("quote")
}
