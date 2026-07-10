package credit

import (
	"fmt"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

// Status represents the credit note lifecycle state.
type Status string

const (
	Issued Status = "issued"
	Void   Status = "void"
)

// CreditNoteLineItem represents a line on a credit note.
type CreditNoteLineItem struct {
	Description string        `json:"description"`
	Amount      int64         `json:"amount"` // cents
	Currency    currency.Type `json:"currency"`
	Quantity    int64         `json:"quantity,omitempty"`
	UnitPrice   int64         `json:"unitPrice,omitempty"`
}

func init() { orm.Register[CreditNote]("credit-note") }

// CreditNote represents a credit issued against an invoice. Credits can be
// applied to customer balance or refunded to the original payment method.
type CreditNote struct {
	mixin.Model[CreditNote]

	InvoiceId  string        `json:"invoiceId"`
	CustomerId string        `json:"customerId"`
	Number     string        `json:"number"` // "CN-0001"
	Amount     int64         `json:"amount"` // total credit in cents
	Currency   currency.Type `json:"currency" orm:"default:usd"`
	Status     Status        `json:"status" orm:"default:issued"`

	// "duplicate" | "fraudulent" | "order_change" | "product_unsatisfactory"
	Reason string `json:"reason,omitempty"`

	LineItems  []CreditNoteLineItem `json:"lineItems,omitempty" datastore:"-"`
	LineItems_ string               `json:"-" datastore:",noindex"`

	// Credit that doesn't correspond to a specific line on the original invoice
	OutOfBandAmount int64 `json:"outOfBandAmount,omitempty"`

	// If applied to customer balance, the balance transaction ID
	CreditBalanceTransaction string `json:"creditBalanceTransaction,omitempty"`

	// If refunded to payment method
	RefundId string `json:"refundId,omitempty"`

	Memo string `json:"memo,omitempty"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (cn *CreditNote) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(cn, ps); err != nil {
		return err
	}

	if len(cn.LineItems_) > 0 {
		if err = json.DecodeBytes([]byte(cn.LineItems_), &cn.LineItems); err != nil {
			return err
		}
	}

	if len(cn.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(cn.Metadata_), &cn.Metadata)
	}

	return err
}

func (cn *CreditNote) Save() (ps []datastore.Property, err error) {
	cn.LineItems_ = string(json.EncodeBytes(&cn.LineItems))
	cn.Metadata_ = string(json.EncodeBytes(&cn.Metadata))
	return datastore.SaveStruct(cn)
}

func (cn *CreditNote) Validator() *val.Validator {
	return nil
}

// MarkVoid voids the credit note.
func (cn *CreditNote) MarkVoid() error {
	if cn.Status != Issued {
		return fmt.Errorf("can only void issued credit notes, current: %s", cn.Status)
	}
	cn.Status = Void
	return nil
}

// SetNumber assigns the credit note number.
func (cn *CreditNote) SetNumber(n int) {
	cn.Number = fmt.Sprintf("CN-%04d", n)
}

func New(db *datastore.Datastore) *CreditNote {
	cn := new(CreditNote)
	cn.Init(db)
	cn.Parent = db.NewKey("synckey", "", 1, nil)
	return cn
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("credit-note")
}
