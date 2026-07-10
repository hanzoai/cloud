package variant

import (
	"math/rand"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/fake"
)

func Fake(db *datastore.Datastore, productId string) *Variant {
	v := New(db)
	v.ProductId = productId
	v.SKU = fake.SKU()
	v.Name = fake.Word()
	v.Available = true
	v.Inventory = rand.Intn(400)
	v.Sold = rand.Intn(400)
	v.Price = currency.Cents(0).Fake()
	v.Taxable = false

	return v
}
