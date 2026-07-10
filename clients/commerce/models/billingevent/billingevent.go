package billingevent

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[BillingEvent]("billing-event") }

// BillingEvent is an append-only record of a billing state change.
// Events are the source of truth for all billing mutations and drive
// webhook delivery to external consumers.
type BillingEvent struct {
	mixin.Model[BillingEvent]

	// Event type, e.g. "payment_intent.succeeded", "invoice.paid", "subscription.updated"
	Type string `json:"type"`

	// Object type, e.g. "payment_intent", "invoice", "subscription"
	ObjectType string `json:"objectType"`

	// ID of the object that changed
	ObjectId string `json:"objectId"`

	// Customer/user this event relates to
	CustomerId string `json:"customerId,omitempty"`

	// Snapshot of the object at event time
	Data  Map    `json:"data,omitempty" datastore:"-"`
	Data_ string `json:"-" datastore:",noindex"`

	// Previous state (for update events)
	PreviousData  Map    `json:"previousData,omitempty" datastore:"-"`
	PreviousData_ string `json:"-" datastore:",noindex"`

	// Whether webhooks have been fully dispatched
	Pending bool `json:"pending" orm:"default:true"`

	// Live vs test mode
	Livemode bool `json:"livemode" orm:"default:true"`
}

func (e *BillingEvent) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(e, ps); err != nil {
		return err
	}

	if len(e.Data_) > 0 {
		if err = json.DecodeBytes([]byte(e.Data_), &e.Data); err != nil {
			return err
		}
	}

	if len(e.PreviousData_) > 0 {
		err = json.DecodeBytes([]byte(e.PreviousData_), &e.PreviousData)
	}

	return err
}

func (e *BillingEvent) Save() (ps []datastore.Property, err error) {
	e.Data_ = string(json.EncodeBytes(&e.Data))
	e.PreviousData_ = string(json.EncodeBytes(&e.PreviousData))
	return datastore.SaveStruct(e)
}

func (e *BillingEvent) Validator() *val.Validator {
	return nil
}

func New(db *datastore.Datastore) *BillingEvent {
	e := new(BillingEvent)
	e.Init(db)
	e.Parent = db.NewKey("synckey", "", 1, nil)
	return e
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("billing-event")
}
