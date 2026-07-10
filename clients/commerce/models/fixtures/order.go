package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/types/country"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/types/productcachedvalues"

	. "github.com/hanzoai/cloud/clients/commerce/models/lineitem"
)

var Order = New("order", func(c *gin.Context) *order.Order {
	db := getNamespaceDb(c)

	u := UserCustomer(c)
	p := Product(c)
	Coupon(c)

	ord := order.New(db)
	ord.UserId = u.Id()
	ord.GetOrCreate("UserId=", ord.UserId)

	ord.ShippingAddress.Name = "Jackson Shirts"
	ord.ShippingAddress.Line1 = "1234 Kansas Drive"
	ord.ShippingAddress.City = "Overland Park"

	ctr, _ := country.FindByISO3166_2("US")
	sd, _ := ctr.FindSubDivision("Kansas")

	ord.ShippingAddress.State = sd.Code
	ord.ShippingAddress.Country = ctr.Codes.Alpha2
	ord.ShippingAddress.PostalCode = "66212"

	ord.Currency = currency.USD
	ord.Items = []LineItem{
		LineItem{
			ProductCachedValues: productcachedvalues.ProductCachedValues{
				Price: currency.Cents(100),
			},
			ProductId: p.Id(),
			Quantity:  20,
		},
	}

	ord.CouponCodes = []string{"SUCH-COUPON", "FREE-DOGE"}
	ord.UpdateAndTally(nil)
	ord.MustPut()

	return ord
})
