package inventorylevel

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[InventoryLevel]("inventorylevel") }

var IgnoreFieldMismatch = datastore.IgnoreFieldMismatch

type InventoryLevel struct {
	mixin.Model[InventoryLevel]

	InventoryItemId  string `json:"inventoryItemId"`
	LocationId       string `json:"locationId"`
	StockedQuantity  int    `json:"stockedQuantity"`
	ReservedQuantity int    `json:"reservedQuantity"`
	IncomingQuantity int    `json:"incomingQuantity"`

	// Arbitrary key/value pairs
	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

// AvailableQuantity returns stock minus reserved.
func (l InventoryLevel) AvailableQuantity() int {
	return l.StockedQuantity - l.ReservedQuantity
}

func (l *InventoryLevel) Load(ps []datastore.Property) (err error) {
	// Prevent duplicate deserialization
	if l.Loaded() {
		return nil
	}

	// Ensure we're initialized
	l.Defaults()

	// Load supported properties
	if err = datastore.LoadStruct(l, ps); err != nil {
		return err
	}

	// Deserialize from datastore
	if len(l.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(l.Metadata_), &l.Metadata)
	}

	return err
}

func (l *InventoryLevel) Save() ([]datastore.Property, error) {
	// Serialize unsupported properties
	l.Metadata_ = string(json.EncodeBytes(&l.Metadata))

	// Save properties
	return datastore.SaveStruct(l)
}

func (l *InventoryLevel) Defaults() {
	l.Metadata = make(Map)
}

func New(db *datastore.Datastore) *InventoryLevel {
	l := new(InventoryLevel)
	l.Init(db)
	l.Defaults()
	return l
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("inventorylevel")
}
