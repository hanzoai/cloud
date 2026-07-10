package reservation

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

var IgnoreFieldMismatch = datastore.IgnoreFieldMismatch

func init() { orm.Register[ReservationItem]("reservation") }

type ReservationItem struct {
	mixin.Model[ReservationItem]

	InventoryItemId string `json:"inventoryItemId"`
	LocationId      string `json:"locationId"`
	LineItemId      string `json:"lineItemId"`
	Quantity        int    `json:"quantity"`
	AllowBackorder  bool   `json:"allowBackorder"`
	Description     string `json:"description"`
	ExternalId      string `json:"externalId"`

	// Arbitrary key/value pairs
	Metadata  Map    `json:"metadata,omitempty" datastore:"-" orm:"default:{}"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (r *ReservationItem) Load(ps []datastore.Property) (err error) {
	// Load supported properties
	if err = datastore.LoadStruct(r, ps); err != nil {
		return err
	}

	// Deserialize from datastore
	if len(r.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(r.Metadata_), &r.Metadata)
	}

	return err
}

func (r *ReservationItem) Save() ([]datastore.Property, error) {
	// Serialize unsupported properties
	r.Metadata_ = string(json.EncodeBytes(&r.Metadata))

	// Save properties
	return datastore.SaveStruct(r)
}

func New(db *datastore.Datastore) *ReservationItem {
	r := new(ReservationItem)
	r.Init(db)
	return r
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("reservation")
}
