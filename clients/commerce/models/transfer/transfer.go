package transfer

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[Transfer]("transfer") }

type Type string

const (
	Stripe Type = "stripe"
)

type Status string

const (
	// Stripe status
	Canceled  Status = "canceled"
	Failed    Status = "failed"
	InTransit Status = "in-transit"
	Paid      Status = "paid"
	Pending   Status = "pending"

	// Failed to submit to stripe
	Error = "error"
)

type StripeAccount struct {
	TransferId string `json:"transferId,omitempty"`
	Type string `json:"type,omitempty"`

	ApplicationFee int64 `json:"applicationFee,omitempty"` // FIXME: Apparently not returned by stripe-go?

	BalanceTransaction int64     `json:"balanceTransaction,omitempty"`
	Created            time.Time `json:"created,omitempty"`
	Date               time.Time `json:"date,omitempty"`
	Description        string    `json:"description,omitempty"`
	Destination        string    `json:"destination,omitempty"`
	DestinationType    string    `json:"destinationType,omitempty"`

	FailureCode    string `json:"failureCode,omitempty"`
	FailureMessage string `json:"failureMessage,omitempty"`

	Reversed bool `json:"reversed,omitempty"`

	SourceTransaction string `json:"sourceTransaction,omitempty"`
	SourceType        string `json:"sourceType,omitempty"`

	StatementDescriptor string `json:"statementDescriptor,omitempty"`
}

type Account struct {
	StripeAccount
}

type Transfer struct {
	mixin.Model[Transfer]

	Account

	AffiliateId string `json:"affiliateId"`
	PartnerId   string `json:"partnerId"`
	FeeId       string `json:"feeId"`

	Currency       currency.Type  `json:"currency"`
	Amount         currency.Cents `json:"amount"`
	AmountReversed currency.Cents `json:"amountReversed,omitempty"`

	Type   Type   `json:"type"`
	Status Status `json:"status" orm:"default:pending"`
	Live   bool   `json:"live,omitempty"`

	Metadata  Map    `json:"metadata" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func New(db *datastore.Datastore) *Transfer {
	t := new(Transfer)
	t.Init(db)
	return t
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("transfer")
}
