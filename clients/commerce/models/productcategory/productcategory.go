// Package productcategory is the hierarchical product category tree (Medusa v2
// parity: product-category). A category may nest under a parent; roots have an
// empty ParentId.
package productcategory

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[ProductCategory]("product-category") }

// ProductCategory is one node in the category tree.
type ProductCategory struct {
	mixin.Model[ProductCategory]

	Name        string `json:"name"`
	Handle      string `json:"handle"` // url slug
	Description string `json:"description" datastore:",noindex"`

	// ParentId is the parent category; empty = root.
	ParentId string `json:"parentId,omitempty"`
	Rank     int    `json:"rank"`

	IsActive   bool `json:"isActive" orm:"default:true"`
	IsInternal bool `json:"isInternal"`

	Metadata  Map    `json:"metadata,omitempty" datastore:"-"`
	Metadata_ string `json:"-" datastore:",noindex"`
}

// IsRoot reports whether this category has no parent.
func (c *ProductCategory) IsRoot() bool { return c.ParentId == "" }

func (c *ProductCategory) Load(ps []datastore.Property) (err error) {
	if err = datastore.LoadStruct(c, ps); err != nil {
		return err
	}
	if len(c.Metadata_) > 0 {
		err = json.DecodeBytes([]byte(c.Metadata_), &c.Metadata)
	}
	return err
}

func (c *ProductCategory) Save() ([]datastore.Property, error) {
	c.Metadata_ = string(json.EncodeBytes(&c.Metadata))
	return datastore.SaveStruct(c)
}

func New(db *datastore.Datastore) *ProductCategory {
	c := new(ProductCategory)
	c.Init(db)
	return c
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("product-category")
}
