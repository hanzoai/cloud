package fixtures

import (
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"

	"github.com/hanzoai/cloud/clients/commerce/models/coupon"
)

var _ = New("karma-coupon", func(c *gin.Context) *coupon.Coupon {
	db := datastore.New(c)

	org := organization.New(db)
	org.Query().Filter("Name=", "karma").Get()

	nsdb := datastore.New(org.Namespaced(db.Context))

	now := time.Now()

	{
		cpn := coupon.New(nsdb)
		cpn.Code_ = strings.ToUpper("DAYNA")
		cpn.GetOrCreate("Code=", cpn.Code_)
		cpn.Name = "Danya's Code"
		cpn.Type = "percent"
		cpn.StartDate = now
		cpn.Once = false
		cpn.Enabled = true
		cpn.Amount = 20

		cpn.MustPut()
	}

	cpn := coupon.New(nsdb)
	cpn.Code_ = strings.ToUpper("SAVEKARMA20")
	cpn.GetOrCreate("Code=", cpn.Code_)
	cpn.Name = "Save Karma"
	cpn.Type = "percent"
	cpn.StartDate = now
	cpn.Once = false
	cpn.Enabled = true
	cpn.Amount = 20

	cpn.MustPut()

	return cpn
})
