// Package productoptionvalue is one value of a product option (e.g. "Small" of
// "Size"). Medusa v2 parity (product-option-value).
package productoptionvalue

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[ProductOptionValue]("product-option-value") }

// ProductOptionValue is a single selectable value of a ProductOption.
type ProductOptionValue struct {
	mixin.Model[ProductOptionValue]

	OptionId string `json:"optionId"`
	Value    string `json:"value"` // "Small", "Red"

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (v *ProductOptionValue) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(v, ps); err != nil {
		return err
	}
	if len(v.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(v.Metadata_), &v.Metadata)
	}
	return err
}

func (v *ProductOptionValue) Save() ([]datastore.Property, error) {
	v.Metadata_ = string(json.EncodeBytes(&v.Metadata))
	return datastore.SaveStruct(v)
}

func New(db *datastore.Datastore) *ProductOptionValue {
	v := new(ProductOptionValue)
	v.Init(db)
	return v
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("product-option-value")
}
