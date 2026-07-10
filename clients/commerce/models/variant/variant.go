package variant

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/types/productcachedvalues"
	"github.com/hanzoai/cloud/clients/commerce/util/val"
	"github.com/hanzoai/orm"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

func init() { orm.Register[Variant]("variant") }

type Variant struct {
	mixin.Model[Variant]
	mixin.Salesforce
	productcachedvalues.ProductCachedValues

	ProductId string `json:"productId"`

	SKU string `json:"sku"`
	UPC string `json:"upc,omitempty"`

	Name string `json:"name"`

	// Variant Media
	Header Media   `json:"header"`
	Image  Media   `json:"image"`
	Media  []Media `json:"media"`

	// Is the variant available
	Available bool `json:"available"`

	// Range in which variant is available. If active, it takes precedent over
	// Available bool.
	Availability Availability `json:"availability"`

	// Inventory int `json:"inventory"`
	Sold int `json:"sold"`

	Options []Option `json:"options"`
}

func (v *Variant) Validator() *val.Validator {
	return val.New().Check("ProductId").Exists().
		Check("SKU").Exists().
		Check("Name").Exists()

	// if v.SKU == "" {
	// 	errs = append(errs, binding.Error{
	// 		FieldNames:     []string{"SKU"},
	// 		Classification: "InputError",
	// 		Message:        "Variant does not have a SKU",
	// 	})
	// }

	// if v.Dimensions == "" {
	// 	errs = append(errs, binding.Error{
	// 		FieldNames:     []string{"Dimensions"},
	// 		Classification: "InputError",
	// 		Message:        "Variant has no given dimensions",
	// 	})
	// }
	// return errs
}

func (v *Variant) Defaults() {
	v.Options = make([]Option, 0)
}

func New(db *datastore.Datastore) *Variant {
	v := new(Variant)
	v.Init(db)
	v.Defaults()
	return v
}

func Query(db *datastore.Datastore) datastore.Query {
	return db.Query("variant")
}
