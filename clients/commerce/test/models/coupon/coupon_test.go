package test

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/coupon"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("models/coupon", t)
}

var (
	ctx ae.Context
	db  *datastore.Datastore
)

// Setup test context
var _ = BeforeSuite(func() {
	ctx = ae.NewContext()
	db = datastore.New(ctx)
})

// Tear-down test context
var _ = AfterSuite(func() {
	ctx.Close()
})

var _ = Describe("Coupon", func() {
	Context("GetById", func() {
		var cpn *coupon.Coupon

		Before(func() {
			cpn = coupon.Fake(db)
			cpn.MustCreate()
		})

		It("should retrieve coupon from datastore by code", func() {
			cpn2 := coupon.New(db)
			cpn2.MustGetById(cpn.Code())
			Expect(cpn2.Code()).To(Equal(cpn2.Code()))
		})
	})
})
