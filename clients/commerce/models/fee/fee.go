package fee

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/commission"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[Fee]("fee") }

type Type string

const (
	Platform  Type = "platform"
	Stripe    Type = "stripe"
	Affiliate Type = "affiliate"
	Partner   Type = "partner"
)

type Status string

const (
	Pending     Status = "pending"
	Disputed    Status = "disputed"
	Transferred Status = "transferred"
	Payable     Status = "payable"
	Refunded    Status = "refunded"
)

type Bitcoin struct {
	FinalTransactionTxId string         `json:"finalTransactionTxId,omitempty"`
	FinalAddress         string         `json:"finalAddress,omitempty"`
	FinalAmount          currency.Cents `json:"finalAmount,omitempty"`
	FinalVOut            int64          `json:"vout,omitempty"`
}

type Ethereum struct {
	FinalTransactionHash string                `json:"finalTransactionHash,omitempty"`
	FinalTransactionCost blockchains.BigNumber `json:"finalTransactionCost,omitempty"`
	FinalAddress         string                `json:"finalAddress,omitempty"`
	FinalAmount          blockchains.BigNumber `json:"finalAmount,omitempty"`
}

type Fee struct {
	mixin.Model[Fee]

	Name string `json:"name"`

	Type        Type   `json:"type"`
	AffiliateId string `json:"affiliateId,omitempty"`
	PartnerId   string `json:"partnerId,omitempty"`

	PaymentId  string `json:"paymentId"`
	TransferId string `json:"transferId"`

	Commission commission.Commission `json:"commission,omitempty"`

	Currency       currency.Type  `json:"currency"`
	Amount         currency.Cents `json:"amount"`
	AmountRefunded currency.Cents `json:"amountRefunded,omitempty"`

	Status Status `json:"status" orm:"default:pending"`

	Ethereum Ethereum `json:"ethereum"`
	Bitcoin  Bitcoin  `json:"bitcoin"`

	// Stripe livemode
	Live bool `json:"live"`

	// Internal testing flag
	Test bool `json:"-"`
}

// New creates a new Fee wired to the given datastore.
func New(db *datastore.Datastore) *Fee {
	f := new(Fee)
	f.Init(db)
	return f
}

// Query returns a datastore query for fees.
func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("fee")
}
