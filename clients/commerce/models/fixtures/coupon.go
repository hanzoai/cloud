package fixtures

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/models/coupon"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
)

const Month = time.Hour * 24 * 30

var Coupon = New("coupon", func(c *gin.Context) *coupon.Coupon {
	db := getNamespaceDb(c)

	now := time.Now()

	p := Product(c)

	cpn := coupon.New(db)
	cpn.Code_ = strings.ToUpper("sad-coupon")
	cpn.GetOrCreate("Code=", cpn.Code_)
	cpn.Name = "Sad Coupon"
	cpn.Type = "flat"
	cpn.EndDate = now.Add(Month)
	cpn.Once = true
	cpn.Enabled = true
	cpn.Amount = 500
	cpn.ProductId = p.Id()

	cpn.MustPut()

	cpn = coupon.New(db)
	cpn.Code_ = strings.ToUpper("such-coupon")
	cpn.GetOrCreate("Code=", cpn.Code_)
	cpn.Name = "Such Coupon"
	cpn.Type = "flat"
	cpn.EndDate = now.Add(Month)
	cpn.Once = true
	cpn.Enabled = true
	cpn.Amount = 500

	cpn.MustPut()

	prod := product.New(db)
	prod.Slug = "doge-shirt"
	prod.GetOrCreate("Slug=", prod.Slug)

	cpn = coupon.New(db)
	cpn.Code_ = strings.ToUpper("FREE-DOGE")
	cpn.GetOrCreate("Code=", cpn.Code_)
	cpn.Name = "Free DogeShirt"
	cpn.Type = "free-item"
	cpn.EndDate = now.Add(Month)
	cpn.Once = true
	cpn.Enabled = true
	cpn.FreeProductId = prod.Id()
	cpn.FreeQuantity = 1

	cpn.MustPut()

	cpn = coupon.New(db)
	cpn.Code_ = strings.ToUpper("NO-DOGE-LEFT-BEHIND")
	cpn.GetOrCreate("Code=", cpn.Code_)
	cpn.Dynamic = true
	cpn.Limit = 1
	cpn.Name = "Free DogeShirt"
	cpn.Type = "free-item"
	cpn.EndDate = now.Add(Month)
	cpn.Once = true
	cpn.Enabled = true
	cpn.FreeProductId = prod.Id()
	cpn.FreeQuantity = 1

	cpn.MustPut()

	// TRYFREE - $5 bot trial + $5 compute credits
	cpn = coupon.New(db)
	cpn.Code_ = "TRYFREE"
	cpn.GetOrCreate("Code=", cpn.Code_)
	cpn.Name = "Try Hanzo Free"
	cpn.Type = "flat"
	cpn.Amount = 500
	cpn.Enabled = true
	cpn.Once = true
	cpn.Filter = "order"
	cpn.CampaignId = "tryfree-growth"

	cpn.MustPut()

	return cpn
})
