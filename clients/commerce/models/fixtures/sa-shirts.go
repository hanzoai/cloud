package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

var _ = New("stoned-shirts", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "stoned"
	org.GetOrCreate("Name=", org.Name)

	nsdb := datastore.New(org.Namespaced(org.Context()))

	datastore.RunInTransaction(nsdb.Context, func(nsdb *datastore.Datastore) error {
		// Create earphone product
		prod := product.New(nsdb)
		prod.Slug = "shirt-men-black-s"
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "Stoned Shirt - Men's - Black - Small"
		prod.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod.Currency = currency.USD
		prod.ListPrice = currency.Cents(3000)
		prod.Price = currency.Cents(2200)
		prod.Inventory = 9000
		prod.Preorder = false
		prod.Hidden = false
		prod.MustUpdate()

		// Create earphone product
		prod1 := product.New(nsdb)
		prod1.Slug = "shirt-men-black-m"
		prod1.GetOrCreate("Slug=", prod.Slug)
		prod1.Name = "Stoned Shirt - Men's - Black - Medium"
		prod1.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod1.Currency = currency.USD
		prod1.ListPrice = currency.Cents(3000)
		prod1.Price = currency.Cents(2200)
		prod1.Inventory = 9000
		prod1.Preorder = false
		prod1.Hidden = false
		prod1.MustUpdate()

		// Create earphone product
		prod2 := product.New(nsdb)
		prod2.Slug = "shirt-men-black-l"
		prod2.GetOrCreate("Slug=", prod.Slug)
		prod2.Name = "Stoned Shirt - Men's - Black - Large"
		prod2.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod2.Currency = currency.USD
		prod2.ListPrice = currency.Cents(3000)
		prod2.Price = currency.Cents(2200)
		prod2.Inventory = 9000
		prod2.Preorder = false
		prod2.Hidden = false
		prod2.MustUpdate()

		// Create earphone product
		prod3 := product.New(nsdb)
		prod3.Slug = "shirt-men-black-xl"
		prod3.GetOrCreate("Slug=", prod.Slug)
		prod3.Name = "Stoned Shirt - Men's - Black - XL"
		prod3.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod3.Currency = currency.USD
		prod3.ListPrice = currency.Cents(3000)
		prod3.Price = currency.Cents(2200)
		prod3.Inventory = 9000
		prod3.Preorder = false
		prod3.Hidden = false
		prod3.MustUpdate()

		// Create earphone product
		prod4 := product.New(nsdb)
		prod4.Slug = "shirt-men-black-xxl"
		prod4.GetOrCreate("Slug=", prod.Slug)
		prod4.Name = "Stoned Shirt - Men's - Black - XXL"
		prod4.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod4.Currency = currency.USD
		prod4.ListPrice = currency.Cents(3000)
		prod4.Price = currency.Cents(2200)
		prod4.Inventory = 9000
		prod4.Preorder = false
		prod4.Hidden = false
		prod4.MustUpdate()

		// Create earphone product
		prod5 := product.New(nsdb)
		prod5.Slug = "shirt-men-black-xxxl"
		prod5.GetOrCreate("Slug=", prod.Slug)
		prod5.Name = "Stoned Shirt - Men's - Black - XXXL"
		prod5.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod5.Currency = currency.USD
		prod5.ListPrice = currency.Cents(3000)
		prod5.Price = currency.Cents(2200)
		prod5.Inventory = 9000
		prod5.Preorder = false
		prod5.Hidden = false
		prod5.MustUpdate()

		return nil
	}, &datastore.TransactionOptions{XG: true})

	datastore.RunInTransaction(nsdb.Context, func(nsdb *datastore.Datastore) error {
		// Create earphone product
		prod := product.New(nsdb)
		prod.Slug = "shirt-women-black-s"
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "Stoned Shirt - Women's - Black - Small"
		prod.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod.Currency = currency.USD
		prod.ListPrice = currency.Cents(3000)
		prod.Price = currency.Cents(2200)
		prod.Inventory = 9000
		prod.Preorder = false
		prod.Hidden = false
		prod.MustUpdate()

		// Create earphone product
		prod1 := product.New(nsdb)
		prod1.Slug = "shirt-women-black-m"
		prod1.GetOrCreate("Slug=", prod.Slug)
		prod1.Name = "Stoned Shirt - Women's - Black - Medium"
		prod1.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod1.Currency = currency.USD
		prod1.ListPrice = currency.Cents(3000)
		prod1.Price = currency.Cents(2200)
		prod1.Inventory = 9000
		prod1.Preorder = false
		prod1.Hidden = false
		prod1.MustUpdate()

		// Create earphone product
		prod2 := product.New(nsdb)
		prod2.Slug = "shirt-women-black-l"
		prod2.GetOrCreate("Slug=", prod.Slug)
		prod2.Name = "Stoned Shirt - Women's - Black - Large"
		prod2.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod2.Currency = currency.USD
		prod2.ListPrice = currency.Cents(3000)
		prod2.Price = currency.Cents(2200)
		prod2.Inventory = 9000
		prod2.Preorder = false
		prod2.Hidden = false
		prod2.MustUpdate()

		// Create earphone product
		prod3 := product.New(nsdb)
		prod3.Slug = "shirt-women-black-xl"
		prod3.GetOrCreate("Slug=", prod.Slug)
		prod3.Name = "Stoned Shirt - Women's - Black - XL"
		prod3.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod3.Currency = currency.USD
		prod3.ListPrice = currency.Cents(3000)
		prod3.Price = currency.Cents(2200)
		prod3.Inventory = 9000
		prod3.Preorder = false
		prod3.Hidden = false
		prod3.MustUpdate()

		// Create earphone product
		prod4 := product.New(nsdb)
		prod4.Slug = "shirt-women-black-xxl"
		prod4.GetOrCreate("Slug=", prod.Slug)
		prod4.Name = "Stoned Shirt - Women's - Black - XXL"
		prod4.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod4.Currency = currency.USD
		prod4.ListPrice = currency.Cents(3000)
		prod4.Price = currency.Cents(2200)
		prod4.Inventory = 9000
		prod4.Preorder = false
		prod4.Hidden = false
		prod4.MustUpdate()

		// Create earphone product
		prod5 := product.New(nsdb)
		prod5.Slug = "shirt-women-black-xxxl"
		prod5.GetOrCreate("Slug=", prod.Slug)
		prod5.Name = "Stoned Shirt - Women's - Black - XXXL"
		prod5.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod5.Currency = currency.USD
		prod5.ListPrice = currency.Cents(3000)
		prod5.Price = currency.Cents(2200)
		prod5.Inventory = 9000
		prod5.Preorder = false
		prod5.Hidden = false
		prod5.MustUpdate()

		return nil
	}, &datastore.TransactionOptions{XG: true})

	datastore.RunInTransaction(nsdb.Context, func(nsdb *datastore.Datastore) error {
		// Create earphone product
		prod := product.New(nsdb)
		prod.Slug = "shirt-men-white-s"
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "Stoned Shirt - Men's - White - Small"
		prod.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod.Currency = currency.USD
		prod.ListPrice = currency.Cents(3000)
		prod.Price = currency.Cents(2200)
		prod.Inventory = 9000
		prod.Preorder = false
		prod.Hidden = false
		prod.MustUpdate()

		// Create earphone product
		prod1 := product.New(nsdb)
		prod1.Slug = "shirt-men-white-m"
		prod1.GetOrCreate("Slug=", prod.Slug)
		prod1.Name = "Stoned Shirt - Men's - White - Medium"
		prod1.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod1.Currency = currency.USD
		prod1.ListPrice = currency.Cents(3000)
		prod1.Price = currency.Cents(2200)
		prod1.Inventory = 9000
		prod1.Preorder = false
		prod1.Hidden = false
		prod1.MustUpdate()

		// Create earphone product
		prod2 := product.New(nsdb)
		prod2.Slug = "shirt-men-white-l"
		prod2.GetOrCreate("Slug=", prod.Slug)
		prod2.Name = "Stoned Shirt - Men's - White - Large"
		prod2.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod2.Currency = currency.USD
		prod2.ListPrice = currency.Cents(3000)
		prod2.Price = currency.Cents(2200)
		prod2.Inventory = 9000
		prod2.Preorder = false
		prod2.Hidden = false
		prod2.MustUpdate()

		// Create earphone product
		prod3 := product.New(nsdb)
		prod3.Slug = "shirt-men-white-xl"
		prod3.GetOrCreate("Slug=", prod.Slug)
		prod3.Name = "Stoned Shirt - Men's - White - XL"
		prod3.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod3.Currency = currency.USD
		prod3.ListPrice = currency.Cents(3000)
		prod3.Price = currency.Cents(2200)
		prod3.Inventory = 9000
		prod3.Preorder = false
		prod3.Hidden = false
		prod3.MustUpdate()

		// Create earphone product
		prod4 := product.New(nsdb)
		prod4.Slug = "shirt-men-white-xxl"
		prod4.GetOrCreate("Slug=", prod.Slug)
		prod4.Name = "Stoned Shirt - Men's - White - XXL"
		prod4.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod4.Currency = currency.USD
		prod4.ListPrice = currency.Cents(3000)
		prod4.Price = currency.Cents(2200)
		prod4.Inventory = 9000
		prod4.Preorder = false
		prod4.Hidden = false
		prod4.MustUpdate()

		// Create earphone product
		prod5 := product.New(nsdb)
		prod5.Slug = "shirt-men-white-xxxl"
		prod5.GetOrCreate("Slug=", prod.Slug)
		prod5.Name = "Stoned Shirt - Men's - White - XXXL"
		prod5.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod5.Currency = currency.USD
		prod5.ListPrice = currency.Cents(3000)
		prod5.Price = currency.Cents(2200)
		prod5.Inventory = 9000
		prod5.Preorder = false
		prod5.Hidden = false
		prod5.MustUpdate()

		return nil
	}, &datastore.TransactionOptions{XG: true})

	datastore.RunInTransaction(nsdb.Context, func(nsdb *datastore.Datastore) error {
		// Create earphone product
		prod := product.New(nsdb)
		prod.Slug = "shirt-women-white-s"
		prod.GetOrCreate("Slug=", prod.Slug)
		prod.Name = "Stoned Shirt - Women's - White - Small"
		prod.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod.Currency = currency.USD
		prod.ListPrice = currency.Cents(3000)
		prod.Price = currency.Cents(2200)
		prod.Inventory = 9000
		prod.Preorder = false
		prod.Hidden = false
		prod.MustUpdate()

		// Create earphone product
		prod1 := product.New(nsdb)
		prod1.Slug = "shirt-women-white-m"
		prod1.GetOrCreate("Slug=", prod.Slug)
		prod1.Name = "Stoned Shirt - Women's - White - Medium"
		prod1.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod1.Currency = currency.USD
		prod1.ListPrice = currency.Cents(3000)
		prod1.Price = currency.Cents(2200)
		prod1.Inventory = 9000
		prod1.Preorder = false
		prod1.Hidden = false
		prod1.MustUpdate()

		// Create earphone product
		prod2 := product.New(nsdb)
		prod2.Slug = "shirt-women-white-l"
		prod2.GetOrCreate("Slug=", prod.Slug)
		prod2.Name = "Stoned Shirt - Women's - White - Large"
		prod2.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod2.Currency = currency.USD
		prod2.ListPrice = currency.Cents(3000)
		prod2.Price = currency.Cents(2200)
		prod2.Inventory = 9000
		prod2.Preorder = false
		prod2.Hidden = false
		prod2.MustUpdate()

		// Create earphone product
		prod3 := product.New(nsdb)
		prod3.Slug = "shirt-women-white-xl"
		prod3.GetOrCreate("Slug=", prod.Slug)
		prod3.Name = "Stoned Shirt - Women's - White - XL"
		prod3.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod3.Currency = currency.USD
		prod3.ListPrice = currency.Cents(3000)
		prod3.Price = currency.Cents(2200)
		prod3.Inventory = 9000
		prod3.Preorder = false
		prod3.Hidden = false
		prod3.MustUpdate()

		// Create earphone product
		prod4 := product.New(nsdb)
		prod4.Slug = "shirt-women-white-xxl"
		prod4.GetOrCreate("Slug=", prod.Slug)
		prod4.Name = "Stoned Shirt - Women's - White - XXL"
		prod4.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod4.Currency = currency.USD
		prod4.ListPrice = currency.Cents(3000)
		prod4.Price = currency.Cents(2200)
		prod4.Inventory = 9000
		prod4.Preorder = false
		prod4.Hidden = false
		prod4.MustUpdate()

		// Create earphone product
		prod5 := product.New(nsdb)
		prod5.Slug = "shirt-women-white-xxxl"
		prod5.GetOrCreate("Slug=", prod.Slug)
		prod5.Name = "Stoned Shirt - Women's - White - XXXL"
		prod5.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
		prod5.Currency = currency.USD
		prod5.ListPrice = currency.Cents(3000)
		prod5.Price = currency.Cents(2200)
		prod5.Inventory = 9000
		prod5.Preorder = false
		prod5.Hidden = false
		prod5.MustUpdate()

		return nil
	}, &datastore.TransactionOptions{XG: true})
	// datastore.RunInTransaction(db.Context, func(db *datastore.Datastore) error {
	// 	// Create earphone product
	// 	prod2 := product.New(nsdb)
	// 	prod2.Slug = "shirt-white"
	// 	prod2.GetOrCreate("Slug=", prod.Slug)
	// 	prod2.Name = ""
	// 	prod2.Description = "The cover, the myth, the legend. The Stoned shirt everyone has asked for."
	// 	prod2.Currency = currency.USD
	// prod2.ListPrice = currency.Cents(3000)
	// prod2.Price = currency.Cents(2200)

	// 	prod2.Inventory = 9000
	// 	prod2.Preorder = false
	// 	prod2.Hidden = false
	// 	prod2.MustUpdate()

	// 	v2s := variant.New(nsdb)
	// 	v2s.ProductId = prod.Id()
	// 	v2s.SKU = prod.Slug + "-S"
	// 	v2s.Name = prod.Name + " - Small"
	// 	v2s.Available = true
	// 	v2s.MustUpdate()

	// 	v2m := variant.New(nsdb)
	// 	v2m.ProductId = prod.Id()
	// 	v2m.SKU = prod.Slug + "-M"
	// 	v2m.Name = prod.Name + " - Medium"
	// 	v2m.Available = true
	// 	v2m.MustUpdate()

	// 	v2l := variant.New(nsdb)
	// 	v2l.ProductId = prod.Id()
	// 	v2l.SKU = prod.Slug + "-L"
	// 	v2l.Name = prod.Name + " - Large"
	// 	v2l.Available = true
	// 	v2l.MustUpdate()

	// 	v2xl := variant.New(nsdb)
	// 	v2xl.ProductId = prod.Id()
	// 	v2xl.SKU = prod.Slug + "-XL"
	// 	v2xl.Name = prod.Name + " - X Large"
	// 	v2xl.Available = true
	// 	v2xl.MustUpdate()

	// 	v2xxl := variant.New(nsdb)
	// 	v2xxl.ProductId = prod.Id()
	// 	v2xxl.SKU = prod.Slug + "-XXL"
	// 	v2xxl.Name = prod.Name + " - XX Large"
	// 	v2xxl.Available = true
	// 	v2xxl.MustUpdate()

	// 	v2xxxl := variant.New(nsdb)
	// 	v2xxxl.ProductId = prod.Id()
	// 	v2xxxl.SKU = prod.Slug + "-XXXL"
	// 	v2xxxl.Name = prod.Name + " - XXX Large"
	// 	v2xxxl.Available = true
	// 	v2xxxl.MustUpdate()
	// }, datastore.TransactionOptions{XG: true})

	return org
})
