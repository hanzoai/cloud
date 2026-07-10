package payment

import (
	"github.com/hanzoai/orm"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/fee"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/accounts"
	"github.com/hanzoai/cloud/clients/commerce/models/types/client"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var kind = "payment"

type Status string

const (
	Cancelled  Status = "cancelled"
	Credit     Status = "credit"
	Disputed   Status = "disputed"
	Failed     Status = "failed"
	Fraudulent Status = "fraudulent"
	Paid       Status = "paid"
	Refunded   Status = "refunded"
	Unpaid     Status = "unpaid"
)


func init() { orm.Register[Payment]("payment") }

type Payment struct {
	mixin.Model[Payment]

	// Deprecated
	Type accounts.Type `json:"type"`

	// Order this payment is associated with
	OrderId string `json:"orderId,omitempty"`

	// User this payment is associated with
	UserId string `json:"userId,omitempty"`

	// Payment source information
	Account accounts.Account `json:"account"`

	// Immutable buyer data from time of payment, may or may not be associated
	// with a user.
	Buyer Buyer `json:"buyer"`

	Currency currency.Type `json:"currency"`

	CampaignId string `json:"campaignId,omitempty"`

	Amount         currency.Cents `json:"amount"`
	AmountRefunded currency.Cents `json:"amountRefunded"`
	Fee            currency.Cents `json:"fee"`
	FeeIds         []string       `json:"fees" datastore:",noindex"`

	AmountTransferred   currency.Cents `json:"-"`
	CurrencyTransferred currency.Type  `json:"-"`

	Description string `json:"description,omitempty"`
	Status      Status `json:"status"`

	// Client's browser, associated info
	Client client.Client `json:"client,omitempty"`

	// Whether this payment has been captured or not
	Captured bool `json:"captured"`

	// Stripe livemode
	Live bool `json:"live"`

	// Internal testing flag
	Test bool `json:"-"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (p *Payment) GetFees() ([]*fee.Fee, error) {
	fees := make([]*fee.Fee, 0)
	if err := fee.Query(p.Datastore()).Filter("PaymentId=", p.Id()).GetModels(&fees); err != nil {
		return nil, err
	}
	return fees, nil
}

func (p *Payment) Load(ps []datastore.Property) (err error) {
	// Ensure we're initialized
	p.Defaults()

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

func (p *Payment) Save() (ps []datastore.Property, err error) {
	// Serialize unsupported properties
	p.Metadata_ = string(json.EncodeBytes(&p.Metadata))

	// Save properties
	return datastore.SaveStruct(p)
}

func (p *Payment) Defaults() {
	p.Status = Unpaid
	p.FeeIds = make([]string, 0)
	p.Metadata = make(Map)
}

func New(db *datastore.Datastore) *Payment {
	p := new(Payment)
	p.Init(db)
	p.Defaults()
	return p
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query(kind)
}
