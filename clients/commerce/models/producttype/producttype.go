// Package producttype is a product type classification (Medusa v2 parity:
// product-type).
package producttype

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[ProductType]("product-type") }

// ProductType classifies products (e.g. "Digital", "Physical").
type ProductType struct {
	mixin.Model[ProductType]

	Value string `json:"value"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (t *ProductType) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(t, ps); err != nil {
		return err
	}
	if len(t.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(t.Metadata_), &t.Metadata)
	}
	return err
}

func (t *ProductType) Save() ([]datastore.Property, error) {
	t.Metadata_ = string(json.EncodeBytes(&t.Metadata))
	return datastore.SaveStruct(t)
}

func New(db *datastore.Datastore) *ProductType {
	t := new(ProductType)
	t.Init(db)
	return t
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("product-type")
}
