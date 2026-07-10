package inventory

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[InventoryItem]("inventoryitem") }

var IgnoreFieldMismatch = datastore.IgnoreFieldMismatch

type InventoryItem struct {
	mixin.Model[InventoryItem]

	SKU              string `json:"sku"`
	OriginCountry    string `json:"originCountry"`
	HSCode           string `json:"hsCode"`
	MidCode          string `json:"midCode"`
	Material         string `json:"material"`
	Weight           int    `json:"weight"`
	Length           int    `json:"length"`
	Height           int    `json:"height"`
	Width            int    `json:"width"`
	RequiresShipping bool   `json:"requiresShipping"`
	Title            string `json:"title"`
	Description      string `json:"description"`
	Thumbnail        string `json:"thumbnail"`

	// Arbitrary key/value pairs
	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (r *InventoryItem) Load(ps []datastore.Property) (err error) {
	// Prevent duplicate deserialization
	if r.Loaded() {
		return nil
	}

	// Ensure we're initialized
	r.Defaults()

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

func (r *InventoryItem) Save() ([]datastore.Property, error) {
	// Serialize unsupported properties
	r.Metadata_ = string(json.EncodeBytes(&r.Metadata))

	// Save properties
	return datastore.SaveStruct(r)
}

func (r *InventoryItem) Defaults() {
	r.Metadata = make(Map)
}

func New(db *datastore.Datastore) *InventoryItem {
	r := new(InventoryItem)
	r.Init(db)
	r.Defaults()
	return r
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("inventoryitem")
}
