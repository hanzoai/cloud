package refund

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[Refund]("refund") }

// Status represents the state of a refund.
type Status string

const (
	Pending   Status = "pending"
	Succeeded Status = "succeeded"
	Failed    Status = "failed"
	Canceled  Status = "canceled"
)

// Refund represents a reversal of a previous payment.
type Refund struct {
	mixin.Model[Refund]

	Amount          int64                  `json:"amount"`
	Currency        currency.Type          `json:"currency"`
	Status          Status                 `json:"status"`
	ProviderRef     string                 `json:"providerRef"`
	Reason          string                 `json:"reason,omitempty"`
	ReceiptNumber   string                 `json:"receiptNumber,omitempty"`
	FailureReason   string                 `json:"failureReason,omitempty"`
	PaymentIntentId string                 `json:"paymentIntentId,omitempty"`
	InvoiceId       string                 `json:"invoiceId,omitempty"`
	Metadata        map[string]interface{} `json:"metadata,omitempty"`
}

// MarkSucceeded transitions the refund to Succeeded.
func (r *Refund) MarkSucceeded() error {
	r.Status = Succeeded
	return nil
}

// MarkFailed transitions the refund to Failed with a reason.
func (r *Refund) MarkFailed(reason string) error {
	r.Status = Failed
	r.FailureReason = reason
	return nil
}

func (r *Refund) Defaults() {
	r.Parent = r.Datastore().NewKey("synckey", "", 1, nil)
	if r.Status == "" {
		r.Status = Pending
	}
}

func New(db *datastore.Datastore) *Refund {
	r := new(Refund)
	r.Init(db)
	r.Defaults()
	return r
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("refund")
}
