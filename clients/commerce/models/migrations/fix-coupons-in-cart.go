package migrations

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/models/cart"
	"github.com/hanzoai/cloud/clients/commerce/models/coupon"

	ds "github.com/hanzoai/cloud/clients/commerce/datastore"
)

var _ = New("fix-coupons-in-cart",
	func(c *gin.Context) []interface{} {
		return NoArgs
	},
	func(db *ds.Datastore, car *cart.Cart) {
		car.CouponCodes = []string{}
		car.Coupons = []coupon.Coupon{}
		car.Put()
	},
)
