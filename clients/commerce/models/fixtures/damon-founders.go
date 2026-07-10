package fixtures

import (
	// "time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/types/country"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/user"

	. "github.com/hanzoai/cloud/clients/commerce/models/lineitem"
)

var _ = New("damon-founders", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "damon"
	org.GetOrCreate("Name=", org.Name)

	nsdb := datastore.New(org.Namespaced(db.Context))

	{
		u := user.New(nsdb)
		u.Email = "wei@lightheartmanagement.com"
		u.GetOrCreate("Email=", u.Email)
		u.FirstName = "Wei"
		u.LastName = "Lin"
		u.Organizations = []string{org.Id()}
		u.PasswordHash, _ = password.Hash("13o5u13oiheqjknasdk")
		u.Put()

		p := product.New(nsdb)
		p.MustGetById("HSP-BGRS")

		ord := order.New(nsdb)
		ord.UserId = u.Id()
		ord.Email = u.Email
		ord.Status = order.Open
		ord.PaymentStatus = payment.Paid
		ord.GetOrCreate("UserId=", ord.UserId)

		ord.ShippingAddress.Name = "Wei Lin"
		ord.ShippingAddress.Line1 = "63 Keefer Place"
		ord.ShippingAddress.Line2 = "2608"
		ord.ShippingAddress.City = "Vancouver"

		ctr, _ := country.FindByISO3166_2("CA")
		sd, _ := ctr.FindSubDivision("British Columbia")

		ord.ShippingAddress.State = sd.Code
		ord.ShippingAddress.Country = ctr.Codes.Alpha2
		ord.ShippingAddress.PostalCode = "V6B6N6"

		ord.Currency = currency.USD
		ord.Items = []LineItem{
			LineItem{
				ProductCachedValues: p.ProductCachedValues,
				ProductId:           p.Id(),
				ProductName:         p.Name,
				ProductSlug:         p.Slug,
				ProductSKU:          p.SKU,
				Quantity:            1,
			},
		}

		ord.UpdateAndTally(nil)
		ord.MustPut()
	}

	{
		u := user.New(nsdb)
		u.Email = "jnott@globalfacesdirect.com"
		u.GetOrCreate("Email=", u.Email)
		u.FirstName = "Jordan"
		u.LastName = "Nott"
		u.Organizations = []string{org.Id()}
		u.PasswordHash, _ = password.Hash("13o5u13oiheqjknasdk")
		u.Put()

		p := product.New(nsdb)
		p.MustGetById("HSP-GRWL")

		ord := order.New(nsdb)
		ord.UserId = u.Id()
		ord.Email = u.Email
		ord.Status = order.Open
		ord.PaymentStatus = payment.Paid
		ord.GetOrCreate("UserId=", ord.UserId)

		ord.ShippingAddress.Name = "Jordan Nott"
		ord.ShippingAddress.Line1 = "1476 Pebble Pl"
		ord.ShippingAddress.City = "Victoria"

		ctr, _ := country.FindByISO3166_2("CA")
		sd, _ := ctr.FindSubDivision("British Columbia")

		ord.ShippingAddress.State = sd.Code
		ord.ShippingAddress.Country = ctr.Codes.Alpha2
		ord.ShippingAddress.PostalCode = "V9B 0T4"

		ord.Currency = currency.USD
		ord.Items = []LineItem{
			LineItem{
				ProductCachedValues: p.ProductCachedValues,
				ProductId:           p.Id(),
				ProductName:         p.Name,
				ProductSlug:         p.Slug,
				ProductSKU:          p.SKU,
				Quantity:            1,
			},
		}

		ord.UpdateAndTally(nil)
		ord.MustPut()
	}

	{
		u := user.New(nsdb)
		u.Email = "Noahli@arkcanada.com"
		u.GetOrCreate("Email=", u.Email)
		u.FirstName = "Noah"
		u.LastName = "Li"
		u.Organizations = []string{org.Id()}
		u.PasswordHash, _ = password.Hash("13o5u13oiheqjknasdk")
		u.Put()

		p := product.New(nsdb)
		p.MustGetById("HSP-WRRS")

		ord := order.New(nsdb)
		ord.UserId = u.Id()
		ord.Email = u.Email
		ord.Status = order.Open
		ord.PaymentStatus = payment.Paid
		ord.GetOrCreate("UserId=", ord.UserId)

		ord.ShippingAddress.Name = "Noah Li"
		ord.ShippingAddress.Line1 = "1281 CORDOVA ST W"
		ord.ShippingAddress.Line2 = "1803"
		ord.ShippingAddress.City = "VANCOUVER"

		ctr, _ := country.FindByISO3166_2("CA")
		sd, _ := ctr.FindSubDivision("British Columbia")

		ord.ShippingAddress.State = sd.Code
		ord.ShippingAddress.Country = ctr.Codes.Alpha2
		ord.ShippingAddress.PostalCode = "V6C 3R5"

		ord.Currency = currency.USD
		ord.Items = []LineItem{
			LineItem{
				ProductCachedValues: p.ProductCachedValues,
				ProductId:           p.Id(),
				ProductName:         p.Name,
				ProductSlug:         p.Slug,
				ProductSKU:          p.SKU,
				Quantity:            1,
			},
		}

		ord.UpdateAndTally(nil)
		ord.MustPut()
	}

	{
		u := user.New(nsdb)
		u.Email = "Justin@genestcapital.com"
		u.GetOrCreate("Email=", u.Email)
		u.FirstName = "Justin"
		u.LastName = "Genest"
		u.Organizations = []string{org.Id()}
		u.PasswordHash, _ = password.Hash("13o5u13oiheqjknasdk")
		u.Put()

		p := product.New(nsdb)
		p.MustGetById("HSP-BRS")

		ord := order.New(nsdb)
		ord.UserId = u.Id()
		ord.Email = u.Email
		ord.Status = order.Open
		ord.PaymentStatus = payment.Paid
		ord.GetOrCreate("UserId=", ord.UserId)

		ord.ShippingAddress.Name = "Justin Genest"
		ord.ShippingAddress.Line1 = "888 Hamilton Street, Vancouver BC, "
		ord.ShippingAddress.Line2 = "905"
		ord.ShippingAddress.City = "Vancouver"

		ctr, _ := country.FindByISO3166_2("CA")
		sd, _ := ctr.FindSubDivision("British Columbia")

		ord.ShippingAddress.State = sd.Code
		ord.ShippingAddress.Country = ctr.Codes.Alpha2
		ord.ShippingAddress.PostalCode = "V6B 5W4"

		ord.Currency = currency.USD
		ord.Items = []LineItem{
			LineItem{
				ProductCachedValues: p.ProductCachedValues,
				ProductId:           p.Id(),
				ProductName:         p.Name,
				ProductSlug:         p.Slug,
				ProductSKU:          p.SKU,
				Quantity:            1,
			},
		}

		ord.UpdateAndTally(nil)
		ord.MustPut()
	}

	{
		u := user.New(nsdb)
		u.Email = "R.Weihs@lmsaws.com"
		u.GetOrCreate("Email=", u.Email)
		u.FirstName = "Rick"
		u.LastName = "Weihs"
		u.Organizations = []string{org.Id()}
		u.PasswordHash, _ = password.Hash("13o5u13oiheqjknasdk")
		u.Put()

		p := product.New(nsdb)
		p.MustGetById("HSP-GRW")

		ord := order.New(nsdb)
		ord.UserId = u.Id()
		ord.Email = u.Email
		ord.Status = order.Open
		ord.PaymentStatus = payment.Paid
		ord.GetOrCreate("UserId=", ord.UserId)

		ord.ShippingAddress.Name = "Rick Weihs"
		ord.ShippingAddress.Line1 = "185A Street"
		ord.ShippingAddress.Line2 = "5278"
		ord.ShippingAddress.City = "Surrey"

		ctr, _ := country.FindByISO3166_2("CA")
		sd, _ := ctr.FindSubDivision("British Columbia")

		ord.ShippingAddress.State = sd.Code
		ord.ShippingAddress.Country = ctr.Codes.Alpha2
		ord.ShippingAddress.PostalCode = "V3S 7A4"

		ord.Currency = currency.USD
		ord.Items = []LineItem{
			LineItem{
				ProductCachedValues: p.ProductCachedValues,
				ProductId:           p.Id(),
				ProductName:         p.Name,
				ProductSlug:         p.Slug,
				ProductSKU:          p.SKU,
				Quantity:            1,
			},
		}

		ord.UpdateAndTally(nil)
		ord.MustPut()
	}

	return org
})
