package subscriptionitem

import (
	"github.com/hanzoai/orm"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/util/val"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var kind = "subscription-item"

// SubscriptionItem represents a single price/product on a subscription.
// Each subscription has one or more items. For metered items, a MeterId
// links usage events to this line. For licensed items, Quantity tracks seats.

func init() { orm.Register[SubscriptionItem](kind) }

type SubscriptionItem struct {
	mixin.Model[SubscriptionItem]

	SubscriptionId string `json:"subscriptionId"`
	PriceId        string `json:"priceId,omitempty"`
	PlanId         string `json:"planId,omitempty"`
	MeterId        string `json:"meterId,omitempty"`

	// Quantity for licensed/per-seat items (0 for metered)
	Quantity int64 `json:"quantity"`

	// "licensed" or "metered"
	BillingMode string `json:"billingMode"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (si *SubscriptionItem) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(si, ps); err != nil {
		return err
	}

	if len(si.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(si.Metadata_), &si.Metadata)
	}

	return err
}

func (si *SubscriptionItem) Save() (ps []datastore.Property, err error) {
	si.Metadata_ = string(json.EncodeBytes(&si.Metadata))
	return datastore.SaveStruct(si)
}

func (si *SubscriptionItem) Validator() *val.Validator {
	return nil
}

func New(db *datastore.Datastore) *SubscriptionItem {
	si := new(SubscriptionItem)
	si.Init(db)
	si.Parent = db.NewKey("synckey", "", 1, nil)
	if si.BillingMode == "" {
		si.BillingMode = "licensed"
	}
	return si
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query(kind)
}
