package usagewatermark

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"
)

func init() { orm.Register[UsageWatermark]("usage-watermark") }

// UsageWatermark records the last aggregated position for a subscription item
// within an invoice period. This prevents double-invoicing of usage events
// and supports late-arriving event handling.
type UsageWatermark struct {
	mixin.Model[UsageWatermark]

	SubscriptionItemId string `json:"subscriptionItemId"`
	MeterId            string `json:"meterId"`
	InvoiceId          string `json:"invoiceId"`

	PeriodStart time.Time `json:"periodStart"`
	PeriodEnd   time.Time `json:"periodEnd"`

	// Usage value that was aggregated into the invoice
	AggregatedValue int64 `json:"aggregatedValue"`

	// Number of events included in the aggregation
	EventCount int64 `json:"eventCount"`

	// Latest event timestamp included in this watermark
	LastEventTimestamp time.Time `json:"lastEventTimestamp"`
}

func (w *UsageWatermark) Load(ps []datastore.Property) (err error) {
	return datastore.LoadStruct(w, ps)
}

func (w *UsageWatermark) Save() (ps []datastore.Property, err error) {
	return datastore.SaveStruct(w)
}

func (w *UsageWatermark) Validator() *val.Validator {
	return nil
}

func New(db *datastore.Datastore) *UsageWatermark {
	w := new(UsageWatermark)
	w.Init(db)
	w.Parent = db.NewKey("synckey", "", 1, nil)
	return w
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("usage-watermark")
}
