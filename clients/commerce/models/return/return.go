package return_

import (
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/fulfillment"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"

	"github.com/hanzoai/cloud/clients/commerce/models/lineitem"
	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[Return]("return") }

type Return struct {
	mixin.Model[Return]

	// Store this was sold from (if any)
	StoreId string `json:"storeId,omitempty"`

	// Associated Crowdstart user or buyer.
	UserId string `json:"userId,omitempty"`

	// Associated order ID, if any
	OrderId string `json:"orderId,omitempty"`

	// External ID
	ExternalID string `json:"externalId,omitempty"`

	// Individual line items
	Items  []lineitem.LineItem `json:"items" datastore:"-"`
	Items_ string              `json:"-" datastore:",noindex"`

	// Fulfillment information
	Fulfillment fulfillment.Fulfillment `json:"fulfillment"`

	// Series of events that have occured relevant to this order
	History []Event `json:"-,omitempty" datastore:",noindex"`

	// Make a custom string for this when we figure out the states...
	Status string `json:"status"`

	// Save notes on order
	Summary string `json:"summary,omitempty"`

	// Arbitrary key/value pairs associated with this order
	Metadata  Map    `json:"metadata" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`

	CancelledAt time.Time `json:"cancelledAt"`
	CompletedAt time.Time `json:"completedAt"`
	ExpectedAt  time.Time `json:"expectedAt"`
	DeliveredAt time.Time `json:"deliveredAt"`
	PickedUpAt  time.Time `json:"pickedUpAt"`
	ProcessedAt time.Time `json:"processedAt"`
	ReturnedAt  time.Time `json:"returnedAt"`
	SubmittedAt time.Time `json:"submittedAt"`
}

func (c *Return) Validator() *val.Validator {
	return val.New()
}

func (c *Return) Load(ps []datastore.Property) (err error) {
	// Prevent duplicate deserialization
	if c.Loaded() {
		return nil
	}

	// Ensure we're initialized
	c.Defaults()

	// Load supported properties
	if err = datastore.LoadStruct(c, ps); err != nil {
		return err
	}

	// Deserialize from datastore
	if len(c.Items_) > 0 {
		err = json.DecodeBytes([]byte(c.Items_), &c.Items)
	}

	if len(c.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(c.Metadata_), &c.Metadata)
	}

	return err
}

func (c *Return) Save() (ps []datastore.Property, err error) {
	// Serialize unsupported properties
	c.Metadata_ = string(json.EncodeBytes(&c.Metadata))
	c.Items_ = string(json.EncodeBytes(c.Items))

	// Save properties
	return datastore.SaveStruct(c)
}

func (r *Return) Defaults() {
	r.Items = make([]lineitem.LineItem, 0)
	r.Metadata = make(Map)
}

func New(db *datastore.Datastore) *Return {
	r := new(Return)
	r.Init(db)
	r.Defaults()
	return r
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("return")
}
