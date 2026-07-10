package test

import (
	"net/http"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/fixtures"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/shippingrates"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/models/taxrates"
	"github.com/hanzoai/cloud/clients/commerce/models/types/country"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/types/georate"
	"github.com/hanzoai/cloud/clients/commerce/util/gincontext"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ginclient"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"

	libraryApi "github.com/hanzoai/cloud/clients/commerce/api/library"
)

func Test(t *testing.T) {
	Setup("api/account", t)
}

var (
	ctx         ae.Context
	cl          *ginclient.Client
	accessToken string
	db          *datastore.Datastore
	org         *organization.Organization
	stor        *store.Store
)

// Setup test context
var _ = BeforeSuite(func() {
	var err error

	publishedRequired := middleware.TokenRequired(permission.Published)

	// Create a new app engine context
	ctx = ae.NewContext()

	// Create mock gin context that we can use with fixtures
	c := gincontext.New(ctx)

	// Run fixtures
	org = fixtures.Organization(c).(*organization.Organization)

	// Setup client and add routes for account API tests.
	cl = ginclient.New(ctx)
	libraryApi.Route(cl.Router, publishedRequired)

	// Create organization for tests, accessToken
	accessToken, _ := org.GetTokenByName("test-published-key")
	err = org.Put()
	Expect(err).NotTo(HaveOccurred())

	// Set authorization header for subsequent requests
	cl.Defaults(func(r *http.Request) {
		r.Header.Set("Authorization", accessToken.String)
	})

	// Save namespaced db
	db = datastore.New(org.Namespaced(ctx))

	// Add some basic rate date
	stor, err = org.GetDefaultStore()
	Expect(err).NotTo(HaveOccurred())

	sr, err := stor.GetShippingRates()
	Expect(err).NotTo(HaveOccurred())
	sr.GeoRates = append(sr.GeoRates, shippingrates.GeoRate{
		GeoRate: georate.New(
			"",
			"",
			"",
			"",
			currency.Cents(0),
			currency.Cents(499),
			0,
			currency.Cents(0),
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
			currency.Cents(0),
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
			0.065,
			currency.Cents(0),
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
			0.2,
			currency.Cents(0),
		),
		TaxShipping: false,
		TaxName:     "VAT",
	})
	tr.MustUpdate()
})

// Tear-down test context
var _ = AfterSuite(func() {
	ctx.Close()
})

var _ = Describe("library", func() {
	Context("shopjs", func() {
		It("Should get everything", func() {
			req := libraryApi.LoadShopJSReq{}
			res := libraryApi.LoadShopJSRes{}

			log.Debug("Response %s", cl.Post("/library/shopjs", req, &res))
			Expect(len(res.Countries)).To(Equal(len(country.Countries)))
			Expect(res.ShippingRates.StoreId).To(Equal(org.DefaultStore))
			Expect(len(res.ShippingRates.GeoRates)).To(Equal(3))
			Expect(res.TaxRates.StoreId).To(Equal(org.DefaultStore))
			Expect(len(res.TaxRates.GeoRates)).To(Equal(5))
			Expect(res.Currency).ToNot(Equal(""))
		})

		It("Should get nothing", func() {
			req := libraryApi.LoadShopJSReq{
				HasCountries:     true,
				HasTaxRates:      true,
				HasShippingRates: true,
				LastChecked:      time.Now().Add(2 * time.Hour),
			}
			res := libraryApi.LoadShopJSRes{}

			log.Debug("Response %s", cl.Post("/library/shopjs", req, &res))
			Expect(res.Countries).To(BeNil())
			Expect(res.ShippingRates).To(BeNil())
			Expect(res.TaxRates).To(BeNil())
		})

		It("Should get out of date", func() {
			req := libraryApi.LoadShopJSReq{
				HasCountries:     true,
				HasTaxRates:      true,
				HasShippingRates: true,
				LastChecked:      time.Now().Add(-2 * time.Hour),
			}
			res := libraryApi.LoadShopJSRes{}

			log.Debug("Response %s", cl.Post("/library/shopjs", req, &res))
			Expect(len(res.Countries)).To(Equal(len(country.Countries)))
			Expect(res.ShippingRates.StoreId).To(Equal(org.DefaultStore))
			Expect(len(res.ShippingRates.GeoRates)).To(Equal(3))
			Expect(res.TaxRates.StoreId).To(Equal(org.DefaultStore))
			Expect(len(res.TaxRates.GeoRates)).To(Equal(5))
		})

		It("Should fail for missing store", func() {
			req := libraryApi.LoadShopJSReq{
				StoreId: "123",
			}

			cl.Post("/library/shopjs", req, nil, 404)
		})

		It("Should return the currency right", func() {
			req := libraryApi.LoadShopJSReq{}
			res := libraryApi.LoadShopJSRes{}

			// Default to USD
			org.Currency = ""
			org.MustUpdate()
			stor.Currency = ""
			stor.MustUpdate()

			log.Debug("Response %s", cl.Post("/library/shopjs", req, &res))
			Expect(string(res.Currency)).To(Equal(string(currency.USD)))

			// Use Order if Store is Blank
			org.Currency = currency.ZMW
			org.MustUpdate()

			log.Debug("Response %s", cl.Post("/library/shopjs", req, &res))
			Expect(string(res.Currency)).To(Equal(string(currency.ZMW)))

			// Use Store is Available
			stor.Currency = currency.AUD
			stor.MustUpdate()

			log.Debug("Response %s", cl.Post("/library/shopjs", req, &res))
			Expect(string(res.Currency)).To(Equal(string(currency.AUD)))
		})
	})
})
