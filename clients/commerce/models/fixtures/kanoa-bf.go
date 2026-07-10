package fixtures

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/coupon"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
)

var _ = New("kanoa-bf", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "kanoa"
	org.GetOrCreate("Name=", org.Name)

	nsCtx := org.Namespaced(db.Context)
	db = datastore.New(nsCtx)

	// Free Cap Product
	prod := product.New(db)
	prod.Slug = "black-friday-cap"
	prod.GetOrCreate("Slug=", prod.Slug)
	prod.Name = "Cap"
	prod.Price = 0
	prod.Currency = currency.USD
	prod.MustPut()

	now := time.Now()

	// Black Friday Coupon
	cpn := coupon.New(db)
	cpn.Code_ = "BLACKANDBLUE"
	cpn.GetOrCreate("Code=", cpn.Code_)
	cpn.Name = "Black Friday Coupon"
	cpn.Type = "free-item"
	cpn.StartDate = now
	cpn.Enabled = true
	cpn.FreeProductId = prod.Id()
	cpn.FreeQuantity = 1
	cpn.MustPut()

	return org
})
