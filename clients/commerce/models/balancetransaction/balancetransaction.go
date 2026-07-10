package balancetransaction

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[BalanceTransaction]("balance-transaction") }

// BalanceTransaction records a single change to a customer's balance.
// Positive amount = credit (adds to balance), negative = debit.
type BalanceTransaction struct {
	mixin.Model[BalanceTransaction]

	CustomerId string        `json:"customerId"`
	Amount     int64         `json:"amount"` // positive = credit, negative = debit
	Currency   currency.Type `json:"currency" orm:"default:usd"`

	// "adjustment" | "credit_note" | "invoice_payment" | "deposit" | "bank_transfer" | "refund"
	Type string `json:"type"`

	Description   string `json:"description,omitempty"`
	InvoiceId     string `json:"invoiceId,omitempty"`
	CreditNoteId  string `json:"creditNoteId,omitempty"`
	SourceRef     string `json:"sourceRef,omitempty"` // external reference
	EndingBalance int64  `json:"endingBalance"`       // balance after this transaction

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (bt *BalanceTransaction) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(bt, ps); err != nil {
		return err
	}

	if len(bt.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(bt.Metadata_), &bt.Metadata)
	}

	return err
}

func (bt *BalanceTransaction) Save() (ps []datastore.Property, err error) {
	bt.Metadata_ = string(json.EncodeBytes(&bt.Metadata))
	return datastore.SaveStruct(bt)
}

func (bt *BalanceTransaction) Validator() *val.Validator {
	return nil
}

func New(db *datastore.Datastore) *BalanceTransaction {
	bt := new(BalanceTransaction)
	bt.Init(db)
	bt.Parent = db.NewKey("synckey", "", 1, nil)
	return bt
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("balance-transaction")
}
