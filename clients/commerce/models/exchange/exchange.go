// Package exchange is the order-exchange domain (Medusa v2 core parity): an
// exchange pairs a return of inbound items on an order with outbound
// replacement items. The net money movement is DifferenceDueCents — positive
// when the customer owes the difference, negative when a refund is due.
package exchange

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[Exchange]("exchange") }

// Exchange lifecycle states.
const (
	StatusRequested = "requested"
	StatusConfirmed = "confirmed"
	StatusCanceled  = "canceled"
)

// ExchangeItem is a single line of an exchange, referencing an order line item
// by ItemId with the quantity being returned (inbound) or shipped (outbound).
type ExchangeItem struct {
	ItemId   string `json:"itemId"`
	Quantity int    `json:"quantity"`
}

// Exchange links a return of InboundItems on an order to OutboundItems shipped
// in their place. DifferenceDueCents is the net owed (>0) or refundable (<0)
// amount. An exchange is open until it is confirmed or canceled.
type Exchange struct {
	mixin.Model[Exchange]

	OrderId string `json:"orderId"`

	// ReturnId links the return that carries the inbound items.
	ReturnId string `json:"returnId,omitempty"`

	// DifferenceDueCents > 0 means the customer owes; < 0 means a refund is due.
	DifferenceDueCents currency.Cents `json:"differenceDueCents"`
	CurrencyCode       currency.Type  `json:"currencyCode" orm:"default:usd"`

	Status string `json:"status" orm:"default:requested"`

	NoNotification bool `json:"noNotification"`

	InboundItems  []ExchangeItem `json:"inboundItems,omitempty" datastore:"-"`
	InboundItems_ string         `json:"-" datastore:",noindex"`

	OutboundItems  []ExchangeItem `json:"outboundItems,omitempty" datastore:"-"`
	OutboundItems_ string         `json:"-" datastore:",noindex"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (e *Exchange) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(e, ps); err != nil {
		return err
	}
	if len(e.InboundItems_) > 0 {
		if err = json.DecodeBytes([]byte(e.InboundItems_), &e.InboundItems); err != nil {
			return err
		}
	}
	if len(e.OutboundItems_) > 0 {
		if err = json.DecodeBytes([]byte(e.OutboundItems_), &e.OutboundItems); err != nil {
			return err
		}
	}
	if len(e.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(e.Metadata_), &e.Metadata)
	}
	return err
}

func (e *Exchange) Save() ([]datastore.Property, error) {
	e.InboundItems_ = string(json.EncodeBytes(&e.InboundItems))
	e.OutboundItems_ = string(json.EncodeBytes(&e.OutboundItems))
	e.Metadata_ = string(json.EncodeBytes(&e.Metadata))
	return datastore.SaveStruct(e)
}

// IsOpen reports whether the exchange is still awaiting confirmation or
// cancellation.
func (e *Exchange) IsOpen() bool {
	return e.Status == StatusRequested
}

func New(db *datastore.Datastore) *Exchange {
	e := new(Exchange)
	e.Init(db)
	return e
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("exchange")
}
