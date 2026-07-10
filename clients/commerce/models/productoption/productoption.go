// Package productoption is a product's option axis (e.g. "Size", "Color") as a
// first-class admin entity — the variant builder composes variants from an
// option's values. Medusa v2 parity (product-option).
package productoption

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[ProductOption]("product-option") }

// ProductOption is one option axis of a product.
type ProductOption struct {
	mixin.Model[ProductOption]

	ProductId string `json:"productId"`
	Title     string `json:"title"` // "Size", "Color"

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (o *ProductOption) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(o, ps); err != nil {
		return err
	}
	if len(o.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(o.Metadata_), &o.Metadata)
	}
	return err
}

func (o *ProductOption) Save() ([]datastore.Property, error) {
	o.Metadata_ = string(json.EncodeBytes(&o.Metadata))
	return datastore.SaveStruct(o)
}

func New(db *datastore.Datastore) *ProductOption {
	o := new(ProductOption)
	o.Init(db)
	return o
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("product-option")
}
