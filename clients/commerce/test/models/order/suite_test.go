package test

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/fixtures"
	"github.com/hanzoai/cloud/clients/commerce/models/lineitem"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/shippingrates"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/models/taxrates"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/types/georate"
	"github.com/hanzoai/cloud/clients/commerce/util/gincontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"

	. "github.com/hanzoai/cloud/clients/commerce/types"
	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("models/order", t)
}

var (
	ctx     ae.Context
	db      *datastore.Datastore
	ord     *order.Order
	stor    *store.Store
	stor2   *store.Store
	stor3   *store.Store
	subProd *product.Product
)

// Setup test context
var _ = BeforeSuite(func() {
	ctx = ae.NewContext()
	db = datastore.New(ctx)

	// Mock gin context that we can use with fixtures
	c := gincontext.New(ctx)
	ord = fixtures.Order(c).(*order.Order)

	// Add a subscription item
	subProd = product.Fake(ord.Datastore())
	subProd.IsSubscribeable = true
	subProd.Interval = Monthly
	subProd.IntervalCount = 1
	subProd.TrialPeriodDays = 1
	subProd.MustPut()

	ord.Items = append(ord.Items, lineitem.LineItem{
		ProductId: subProd.Id(),
		Quantity:  1,
	})

	stor = store.New(ord.Datastore())
	stor.MustCreate()

	stor2 = store.New(ord.Datastore())
	stor2.MustCreate()

	sr, err := stor.GetShippingRates()
	Expect(err).NotTo(HaveOccurred())

	sr.GeoRates = append(sr.GeoRates, shippingrates.GeoRate{
		GeoRate: georate.New(
			"",
			"",
			"",
			"",
			currency.Cents(0),
			currency.Cents(0),
			0.1,
			currency.Cents(499),
		),
		ShippingName: "SHIPPING",
	})
	sr.MustUpdate()

	tr, err := stor.GetTaxRates()
	Expect(err).NotTo(HaveOccurred())
	tr.GeoRates = append(tr.GeoRates, taxrates.GeoRate{
		GeoRate: georate.New(
			"US",
			"KS",
			"",
			"66212",
			currency.Cents(0),
			currency.Cents(0),
			0.0885,
			currency.Cents(1),
		),
		TaxShipping: false,
		TaxName:     "TEST TAX",
	})
	tr.GeoRates = append(tr.GeoRates, taxrates.GeoRate{
		GeoRate: georate.New(
			"US",
			"KS",
			"",
			"",
			currency.Cents(0),
			currency.Cents(0),
			0.0885,
			currency.Cents(1),
		),
		TaxShipping: false,
		TaxName:     "TEST TAX",
	})
	tr.GeoRates = append(tr.GeoRates, taxrates.GeoRate{
		GeoRate: georate.New(
			"GB",
			"",
			"",
			"",
			currency.Cents(0),
			currency.Cents(0),
			0.20,
			currency.Cents(0),
		),
		TaxShipping: false,
		TaxName:     "VAT",
	})
	tr.MustUpdate()

	fixtures.Coupon(c)

	stor2 = store.New(ord.Datastore())
	stor2.MustCreate()

	sr2, err := stor2.GetShippingRates()
	Expect(err).NotTo(HaveOccurred())

	sr2.MustDelete()

	tr2, err := stor2.GetTaxRates()
	Expect(err).NotTo(HaveOccurred())

	stor3 = store.New(ord.Datastore())
	stor3.MustCreate()
	stor3.Currency = currency.ETH

	price := currency.Cents(1234)

	stor3.Listings = make(map[string]store.Listing)
	stor3.Listings[ord.Items[0].ProductId] = store.Listing{Price: &price}

	tr2.MustDelete()
})

// Tear-down test context
var _ = AfterSuite(func() {
	ctx.Close()
})
