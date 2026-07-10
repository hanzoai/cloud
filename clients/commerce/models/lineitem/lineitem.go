package lineitem

import (
	"fmt"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/mixin"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/types/productcachedvalues"
	"github.com/hanzoai/cloud/clients/commerce/models/variant"

	. "github.com/hanzoai/cloud/clients/commerce/types"
)

type LineItem struct {
	mixin.Salesforce
	productcachedvalues.ProductCachedValues

	CollectionId string `json:"collectionId,omitempty"`

	Product     *product.Product `json:"-" datastore:"-"`
	ProductId   string           `json:"productId,omitempty"`
	ProductName string           `json:"productName,omitempty"`
	ProductSlug string           `json:"productSlug,omitempty"`
	ProductSKU  string           `json:"productSKU,omitempty"`
	// shipwire
	ExternalSKU string `json:"sku,omitempty"`

	Variant     *variant.Variant `json:"-" datastore:"-"`
	VariantId   string           `json:"variantId,omitempty"`
	VariantName string           `json:"variantName,omitempty"`
	VariantSKU  string           `json:"variantSKU,omitempty"`

	// Number of units
	Quantity int `json:"quantity"`

	// Item should be considered free due to coupon being applied or whatnot.
	Free bool `json:"free,omitempty"`

	// Non-user party which added this lineitem (coupon or otherwise).
	AddedBy string `json:"addedBy,omitempty"`
}

func (li LineItem) Id() string {
	if li.VariantId != "" {
		return li.VariantId
	}
	return li.ProductId
}

func (li LineItem) SKU() string {
	if li.VariantSKU != "" {
		return li.VariantSKU
	}
	return li.ProductSKU
}

func (li LineItem) ToMap() map[string]interface{} {
	vals := make(map[string]interface{})

	vals["CollectionId"] = li.CollectionId
	vals["ProductId"] = li.ProductId
	vals["VariantId"] = li.VariantId
	vals["Quantity"] = int64(li.Quantity)
	vals["Price"] = int64(li.Price)
	vals["Taxable"] = li.Taxable
	vals["Free"] = li.Free
	vals["AddedBy"] = li.AddedBy

	vals["IsSubscribeable"] = bool(li.IsSubscribeable)
	vals["Interval"] = string(li.Interval)
	vals["IntervalCount"] = int64(li.IntervalCount)
	vals["TrialPeriodDays"] = int64(li.TrialPeriodDays)

	return vals
}

func (li LineItem) TotalPrice() currency.Cents {
	return li.Price * currency.Cents(li.Quantity)
}

func (li LineItem) DisplayPrice(t currency.Type) string {
	return DisplayPrice(t, li.Price)
}

func (li LineItem) DisplayTotalPrice(t currency.Type) string {
	return DisplayPrice(t, li.TotalPrice())
}

// Check if id is valid identifier for this line item
func (li LineItem) HasId(id string) bool {
	if id == li.ProductId || id == li.VariantId || id == li.ProductSlug || id == li.VariantSKU {
		return true
	}

	return false
}

func (li LineItem) DisplayName() string {
	if li.VariantName != "" {
		return li.VariantName
	}

	if li.ProductName != "" {
		return li.ProductName
	}

	return li.DisplayId()
}

func (li LineItem) DisplayId() string {
	if li.VariantSKU != "" {
		return li.VariantSKU
	}
	return li.ProductSlug
}

// Returns the key and entity represented by this line item.
func (li *LineItem) Entity(db *datastore.Datastore) (datastore.Key, mixin.Entity, error) {
	if li.VariantId != "" {
		li.Variant = variant.New(db)
		li.Variant.SetKey(li.VariantId)
		return li.Variant.Key(), li.Variant, nil
	}

	if li.ProductId != "" {
		li.Product = product.New(db)
		li.Product.SetKey(li.ProductId)
		return li.Product.Key(), li.Product, nil
	}

	if li.VariantSKU != "" {
		li.Variant = variant.New(db)
		ok, err := li.Variant.Query().Filter("SKU=", li.VariantSKU).GetKey()
		if err != nil {
			return nil, nil, err
		}

		if !ok {
			return nil, nil, fmt.Errorf("Variant for lineitem does not exist: %v", li)
		}

		return li.Variant.Key(), li.Variant, nil
	}

	if li.ProductSlug != "" {
		li.Product = product.New(db)
		ok, err := li.Product.Query().Filter("Slug=", li.ProductSlug).GetKey()
		if err != nil {
			return nil, nil, err
		}

		if !ok {
			return nil, nil, fmt.Errorf("Product for lineitem does not exist: %v", li)
		}

		if ok {
			return li.Product.Key(), li.Product, nil
		}
	}

	return nil, nil, LineItemError{li}
}

// Set product by id
func (li *LineItem) SetProduct(db *datastore.Datastore, id string, quantity int) error {
	prod := product.New(db)
	if err := prod.GetById(id); err != nil {
		return err
	}

	li.Product = prod
	li.ProductId = prod.Id()
	li.ProductName = prod.Name
	li.ProductSlug = prod.Slug
	li.Quantity = quantity
	li.ProductCachedValues = prod.ProductCachedValues

	return nil
}

// Set variant by id
func (li *LineItem) SetVariant(db *datastore.Datastore, id string, quantity int) error {
	vari := variant.New(db)
	if err := vari.GetById(id); err != nil {
		return err
	}

	li.Variant = vari
	li.VariantId = vari.Id()
	li.VariantName = vari.Name
	li.VariantSKU = vari.SKU
	li.Quantity = quantity
	li.ProductCachedValues = vari.ProductCachedValues

	return nil
}

func (li *LineItem) Update() {
	if li.Product != nil {
		li.ProductCachedValues = li.Product.ProductCachedValues
		if li.ProductId == "" {
			li.ProductId = li.Product.Id()
		}
	}

	if li.Variant != nil {
		li.ProductCachedValues = li.Variant.ProductCachedValues
		if li.VariantId == "" {
			li.VariantId = li.Variant.Id()
		}
	}
}

func (li LineItem) String() string {
	if li.VariantName != "" {
		return fmt.Sprintf("%v", li.VariantName)
	}

	if li.VariantSKU != "" {
		return fmt.Sprintf("%v", li.VariantSKU)
	}

	if li.VariantId != "" {
		return fmt.Sprintf("%v", li.VariantId)
	}

	if li.ProductName != "" {
		return fmt.Sprintf("%v", li.ProductName)
	}

	if li.ProductSlug != "" {
		return fmt.Sprintf("%v", li.ProductSlug)
	}

	if li.ProductId != "" {
		return fmt.Sprintf("%v", li.ProductId)
	}

	return fmt.Sprintf("LineItem{Quantity:%d}", li.Quantity)
}

// func (li LineItem) Validate(req *http.Request, errs binding.Errors) binding.Errors {
// 	if li.SKU() == "" {
// 		errs = append(errs, binding.Error{
// 			FieldNames:     []string{"Variant.SKU"},
// 			Classification: "InputError",
// 			Message:        "SKU cannot be empty.",
// 		})
// 	}

// 	if li.Quantity < 1 {
// 		errs = append(errs, binding.Error{
// 			FieldNames:     []string{"Quantity"},
// 			Classification: "InputError",
// 			Message:        "Quantity cannot be less than 1.",
// 		})
// 	}

// 	return errs
// }

// Displays nice "/" delimited variant information.
// func (li LineItem) DisplayShortDescription() string {
// 	opts := []string{}
// 	for _, opt := range []string{li.Product.Title, li.Variant.Color, li.Variant.Style, li.Variant.Size} {
// 		if opt != "" {
// 			opts = append(opts, opt)
// 		}
// 	}
// 	if len(opts) > 0 {
// 		return strings.Join(opts, " / ")
// 	} else {
// 		return li.SKU()
// 	}
// }
