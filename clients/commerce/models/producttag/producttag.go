// Package producttag is a free-form product tag (Medusa v2 parity: product-tag).
package producttag

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[ProductTag]("product-tag") }

// ProductTag is a taggable label for products.
type ProductTag struct {
	mixin.Model[ProductTag]

	Value string `json:"value"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

func (t *ProductTag) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(t, ps); err != nil {
		return err
	}
	if len(t.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(t.Metadata_), &t.Metadata)
	}
	return err
}

func (t *ProductTag) Save() ([]datastore.Property, error) {
	t.Metadata_ = string(json.EncodeBytes(&t.Metadata))
	return datastore.SaveStruct(t)
}

func New(db *datastore.Datastore) *ProductTag {
	t := new(ProductTag)
	t.Init(db)
	return t
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("product-tag")
}
