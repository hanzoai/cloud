package test

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/paypal"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"

	. "github.com/hanzoai/cloud/clients/commerce/models/lineitem"
	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("thirdparty/paypal", t)
}

var (
	ctx    ae.Context
	org    *organization.Organization
	usr    *user.User
	ord    *order.Order
	pay    *payment.Payment
	client *paypal.Client
)

var _ = BeforeSuite(func() {
	ctx = ae.NewContext()

	db := datastore.New(ctx)

	usr = user.New(db)
	// usr.PaypalEmail = "dev@hanzo.ai"

	org = organization.New(db)
	org.Paypal.ConfirmUrl = "http://hanzo.ai"
	org.Paypal.CancelUrl = "http://hanzo.ai"

	org.Paypal.Test.Email = "dev@hanzo.ai"
	org.Paypal.Test.SecurityUserId = "dev@hanzo.ai"
	org.Paypal.Test.ApplicationId = "APP-80W284485P519543T"
	org.Paypal.Test.SecurityPassword = ""
	org.Paypal.Test.SecuritySignature = ""
	org.MustCreate()

	ord = order.New(db)
	ord.Items = make([]LineItem, 1)
	ord.Items[0] = LineItem{
		ProductId:   "Test Product Id",
		ProductName: "Test Product Name",
		ProductSlug: "Test Product Slug",
		// :       100,
		Quantity: 1,
	}
	ord.Currency = currency.USD
	ord.Tax = 1
	ord.Shipping = 2
	ord.Total = 103

	pay = payment.New(db)
	pay.Amount = 103
	pay.Currency = currency.USD
	pay.Client.Ip = "64.136.209.186"
	platformFees, partnerFees := org.Pricing()

	var err error
	pay.Fee, _, err = ord.CalculateFees(platformFees, partnerFees)
	Expect(err).ToNot(HaveOccurred())
	client = paypal.New(ctx)
})

var _ = AfterSuite(func() {
	ctx.Close()
})

var _ = Describe("paypal.GetPayKey", func() {
	Context("Get Paypal PayKey", func() {
		It("Should succeed in the normal case", func() {
			key, err := client.GetPayKey(pay, ord, org)
			Expect(err).ToNot(HaveOccurred())
			Expect(key).ToNot(Equal(""))
		})
	})
})
