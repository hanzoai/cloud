package test

import (
	"github.com/hanzoai/cloud/clients/commerce/models/lineitem"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/rand"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var _ = Describe("Order.UpdateAndTally", func() {
	Context("Storeless (Deprecated)", func() {
		// Make sure out of order execution works because tax is left alone in
		// deprecated flow
		BeforeEach(func() {
			// Scramble currency values so we know they are being replaced
			for i := range ord.Coupons {
				ord.Coupons[i].Amount = rand.Int()
			}

			for i := range ord.Items {
				ord.Items[i].Price = currency.Cents(rand.Int64())
			}

			ord.LineTotal = currency.Cents(rand.Int64())
			ord.Discount = currency.Cents(rand.Int64())
			ord.Subtotal = currency.Cents(rand.Int64())
			ord.Tax = 0      //currency.Cents(rand.Int64())
			ord.Shipping = 0 //currency.Cents(rand.Int64())
			ord.Total = currency.Cents(rand.Int64())
			ord.TokenSaleId = ""
			ord.WalletId = ""
			ord.WalletPassphrase = ""
			ord.Mode = order.DefaultMode
		})

		It("Should UpdateAndTally", func() {
			ord.CouponCodes = []string{}
			err := ord.UpdateAndTally(nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(ord.Subtotal).To(Equal(currency.Cents(50000)))
			Expect(ord.Total).To(Equal(currency.Cents(50000)))
		})

		It("Should UpdateAndTally With No Tax or ShippingRates without crashing", func() {
			sr, _ := stor2.GetShippingRates()
			Expect(sr).To(BeNil())

			tr, _ := stor2.GetTaxRates()
			Expect(tr).To(BeNil())

			ord.CouponCodes = []string{}
			err := ord.UpdateAndTally(stor2)
			Expect(err).ToNot(HaveOccurred())
			Expect(ord.Subtotal).To(Equal(currency.Cents(50000)))

			tax := currency.Cents(0)
			shipping := currency.Cents(0)

			Expect(ord.Tax).To(Equal(tax))
			Expect(ord.Shipping).To(Equal(shipping))
			Expect(ord.Total).To(Equal(ord.Subtotal + tax + shipping))
		})

		It("Should UpdateAndTally With Coupon", func() {
			ord.CouponCodes = []string{"such-coupon"}
			err := ord.UpdateAndTally(nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(ord.Subtotal).To(Equal(currency.Cents(49500)))
			Expect(ord.Total).To(Equal(currency.Cents(49500)))
		})

		It("Should UpdateAndTally And Dedupe Coupons", func() {
			ord.CouponCodes = []string{"such-coupon", "such-coupon"}
			err := ord.UpdateAndTally(nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(ord.CouponCodes)).To(Equal(1))
			Expect(ord.CouponCodes[0]).To(Equal("such-coupon"))
			Expect(ord.Subtotal).To(Equal(currency.Cents(49500)))
			Expect(ord.Total).To(Equal(currency.Cents(49500)))
		})

		It("Should UpdateAndTally Only Applicable Coupons", func() {
			ord2 := order.New(ord.Datastore())
			ord2.CouponCodes = []string{"sad-coupon"}
			ord2.Items = []lineitem.LineItem{{ProductSlug: "doge-shirt", Quantity: 1}}
			err := ord2.UpdateAndTally(nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(ord2.Subtotal).To(Equal(currency.Cents(2000)))
			Expect(ord2.Total).To(Equal(currency.Cents(2000)))

			ord2 = order.New(ord.Datastore())
			ord2.CouponCodes = []string{"sad-coupon"}
			ord2.Items = []lineitem.LineItem{{ProductSlug: "sad-keanu-shirt", Quantity: 1}}
			err = ord2.UpdateAndTally(nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(ord2.Subtotal).To(Equal(currency.Cents(2000)))
			Expect(ord2.Total).To(Equal(currency.Cents(2000)))
		})

		It("Should Serialize Line Items", func() {
			ord2 := order.New(ord.Datastore())
			ord2.CouponCodes = []string{"sad-coupon"}
			ord2.Items = []lineitem.LineItem{{ProductSlug: "doge-shirt", ProductName: "Doge Shirt", Quantity: 1}}

			memo := ord2.DescriptionLong()
			Expect(memo).To(Equal("Doge Shirt (doge-shirt) x 1\n"))
		})
	})

	Context("Store", func() {
		BeforeEach(func() {
			// Scramble currency values so we know they are being replaced
			for i := range ord.Coupons {
				ord.Coupons[i].Amount = rand.Int()
			}

			for i := range ord.Items {
				ord.Items[i].Price = currency.Cents(rand.Int64())
			}

			ord.LineTotal = currency.Cents(rand.Int64())
			ord.Discount = currency.Cents(rand.Int64())
			ord.Subtotal = currency.Cents(rand.Int64())
			ord.Tax = currency.Cents(rand.Int64())
			ord.Shipping = currency.Cents(rand.Int64())
			ord.Total = currency.Cents(rand.Int64())
			ord.TokenSaleId = ""
			ord.WalletId = ""
			ord.WalletPassphrase = ""
			ord.Mode = order.DefaultMode
		})

		It("Should UpdateAndTally", func() {
			ord.CouponCodes = []string{}
			err := ord.UpdateAndTally(stor)
			Expect(err).ToNot(HaveOccurred())
			Expect(ord.Subtotal).To(Equal(currency.Cents(50000)))

			tax := 1 + currency.Cents(float64(ord.Subtotal)*0.0885)
			shipping := 499 + currency.Cents(float64(ord.Subtotal)*0.1)

			Expect(ord.Tax).To(Equal(tax))
			Expect(ord.Shipping).To(Equal(shipping))
			Expect(ord.Total).To(Equal(ord.Subtotal + tax + shipping))
		})

		It("Should UpdateAndTally Price Overrides", func() {
			ord.CouponCodes = []string{}
			err := ord.UpdateAndTally(stor3)
			Expect(err).ToNot(HaveOccurred())
			Expect(ord.Subtotal).To(Equal(currency.Cents(24680)))
			Expect(ord.Currency).To(Equal(currency.ETH))

			Expect(ord.Total).To(Equal(ord.Subtotal))

		})

		It("Should UpdateAndTally with Provided Subtotal for Contributions", func() {
			ord.CouponCodes = []string{}
			ord.Mode = order.ContributionMode
			subTotal := ord.Subtotal
			err := ord.UpdateAndTally(stor)
			Expect(err).ToNot(HaveOccurred())
			Expect(ord.Subtotal).To(Equal(subTotal))

			tax := 1 + currency.Cents(float64(ord.Subtotal)*0.0885)
			shipping := 499 + currency.Cents(float64(ord.Subtotal)*0.1)

			Expect(ord.Tax).To(Equal(tax))
			Expect(ord.Shipping).To(Equal(shipping))
			Expect(ord.Total).To(Equal(ord.Subtotal + tax + shipping))
		})

		It("Should UpdateAndTally with Provided Subtotal for Deposit", func() {
			ord.CouponCodes = []string{}
			ord.Mode = order.ContributionMode
			subTotal := ord.Subtotal
			err := ord.UpdateAndTally(stor)
			Expect(err).ToNot(HaveOccurred())
			Expect(ord.Subtotal).To(Equal(subTotal))

			tax := 1 + currency.Cents(float64(ord.Subtotal)*0.0885)
			shipping := 499 + currency.Cents(float64(ord.Subtotal)*0.1)

			Expect(ord.Tax).To(Equal(tax))
			Expect(ord.Shipping).To(Equal(shipping))
			Expect(ord.Total).To(Equal(ord.Subtotal + tax + shipping))
		})

		It("Should UpdateAndTally with Provided Subtotal for TokenSales", func() {
			ord.CouponCodes = []string{}
			ord.TokenSaleId = "1234"
			subTotal := ord.Subtotal
			err := ord.UpdateAndTally(stor)
			Expect(err).ToNot(HaveOccurred())
			Expect(ord.Subtotal).To(Equal(subTotal))

			tax := 1 + currency.Cents(float64(ord.Subtotal)*0.0885)
			shipping := 499 + currency.Cents(float64(ord.Subtotal)*0.1)

			Expect(ord.Tax).To(Equal(tax))
			Expect(ord.Shipping).To(Equal(shipping))
			Expect(ord.Total).To(Equal(ord.Subtotal + tax + shipping))
		})

		It("Should UpdateAndTally With Coupon", func() {
			ord.CouponCodes = []string{"such-coupon"}
			err := ord.UpdateAndTally(stor)
			Expect(err).ToNot(HaveOccurred())

			Expect(ord.Subtotal).To(Equal(currency.Cents(49500)))

			tax := 1 + currency.Cents(float64(ord.Subtotal+ord.Discount)*0.0885)
			shipping := 499 + currency.Cents(float64(ord.Subtotal)*0.1)

			Expect(ord.Tax).To(Equal(tax))
			Expect(ord.Shipping).To(Equal(shipping))
			Expect(ord.Total).To(Equal(ord.Subtotal + tax + shipping))
		})

		It("Should UpdateAndTally And Dedupe Coupons", func() {
			ord.CouponCodes = []string{"such-coupon", "such-coupon"}
			err := ord.UpdateAndTally(stor)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(ord.CouponCodes)).To(Equal(1))
			Expect(ord.CouponCodes[0]).To(Equal("such-coupon"))

			Expect(ord.Subtotal).To(Equal(currency.Cents(49500)))

			tax := 1 + currency.Cents(float64(ord.Subtotal+ord.Discount)*0.0885)
			shipping := 499 + currency.Cents(float64(ord.Subtotal)*0.1)

			Expect(ord.Tax).To(Equal(tax))
			Expect(ord.Shipping).To(Equal(shipping))
			Expect(ord.Total).To(Equal(ord.Subtotal + tax + shipping))
		})

		It("Should UpdateAndTally Only Applicable Coupons", func() {
			ord2 := order.New(ord.Datastore())
			ord2.CouponCodes = []string{"sad-coupon"}
			ord2.Items = []lineitem.LineItem{{ProductSlug: "doge-shirt", Quantity: 1}}
			ord2.ShippingAddress.Country = "us"
			ord2.ShippingAddress.State = "ks"
			ord2.ShippingAddress.PostalCode = "66212"

			err := ord2.UpdateAndTally(stor)
			Expect(err).ToNot(HaveOccurred())
			Expect(ord2.Subtotal).To(Equal(currency.Cents(2000)))

			tax := 1 + currency.Cents(float64(ord2.Subtotal)*0.0885)
			shipping := 499 + currency.Cents(float64(ord2.Subtotal)*0.1)

			Expect(ord2.Tax).To(Equal(tax))
			Expect(ord2.Shipping).To(Equal(shipping))
			Expect(ord2.Total).To(Equal(ord2.Subtotal + tax + shipping))

			ord2 = order.New(ord.Datastore())
			ord2.CouponCodes = []string{"sad-coupon"}
			ord2.Items = []lineitem.LineItem{{ProductSlug: "sad-keanu-shirt", Quantity: 1}}
			ord2.ShippingAddress.Country = "us"
			ord2.ShippingAddress.State = "ks"
			ord2.ShippingAddress.PostalCode = "66212"

			err = ord2.UpdateAndTally(stor)
			Expect(err).ToNot(HaveOccurred())
			Expect(ord2.Subtotal).To(Equal(currency.Cents(2000)))

			tax = 1 + currency.Cents(float64(ord2.Subtotal)*0.0885)
			shipping = 499 + currency.Cents(float64(ord2.Subtotal)*0.1)

			Expect(ord2.Tax).To(Equal(tax))
			Expect(ord2.Shipping).To(Equal(shipping))
			Expect(ord2.Total).To(Equal(ord2.Subtotal + tax + shipping))
		})
	})
})
