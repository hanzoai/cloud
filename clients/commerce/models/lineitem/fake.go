package lineitem

import (
	"math/rand"

	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/variant"
)

func Fake(item interface{}) LineItem {
	var li LineItem

	switch v := item.(type) {
	case *product.Product:
		li.ProductId = v.Id()
		li.ProductName = v.Name
		li.ProductSlug = v.Slug
		li.Price = v.Price
	case *variant.Variant:
		li.VariantId = v.Id()
		li.VariantName = v.Name
		li.VariantSKU = v.SKU
		li.Price = v.Price
	default:
		panic("Unsupported item")
	}

	li.Quantity = rand.Intn(5) + 1
	li.Taxable = false
	return li
}
