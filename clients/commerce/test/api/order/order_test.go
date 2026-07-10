package test

import (
	"strings"

	"github.com/icrowley/fake"

	"github.com/hanzoai/cloud/clients/commerce/models/lineitem"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/variant"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var _ = Describe("order", func() {
	var normalize = func(s string) string {
		return strings.ToLower(strings.TrimSpace(s))
	}

	Context("New order", func() {
		req := new(order.Order)
		res := new(order.Order)

		Before(func() {
			prod := product.Fake(db)
			prod.MustCreate()
			vari := variant.Fake(db, prod.Id())
			vari.MustCreate()
			li := lineitem.Fake(vari)
			req = order.Fake(db, li)
			res = order.New(db)

			// Create new order
			cl.Post("/order", req, res)
		})

		It("Should create new orders", func() {
			Expect(res.Email).To(Equal(normalize(req.Email)))
			Expect(res.Status).To(Equal(req.Status))
			Expect(res.PaymentStatus).To(Equal(req.PaymentStatus))
			Expect(res.Preorder).To(Equal(req.Preorder))
			Expect(res.Unconfirmed).To(Equal(req.Unconfirmed))
			Expect(res.Currency).To(Equal(req.Currency))
			//TODO: These are coming back as zero on the POST request, let's figure out why
			//Expect(res.LineTotal).To(Equal(req.LineTotal))
			//Expect(res.Discount).To(Equal(req.Discount))
			//Expect(res.Subtotal).To(Equal(req.Subtotal))
			//Expect(res.Shipping).To(Equal(req.Shipping))
			//Expect(res.Tax).To(Equal(req.Tax))
			//Expect(res.Total).To(Equal(req.Total))
			//TODO: Prices on the items are also coming back as zero.
			//Expect(res.Items).To(Equal(req.Items))
			Expect(res.Company).To(Equal(req.Company))
			Expect(res.Balance).To(Equal(req.Balance))
			Expect(res.Paid).To(Equal(req.Paid))
			Expect(res.Refunded).To(Equal(req.Refunded))
			Expect(res.Company).To(Equal(req.Company))
			Expect(res.BillingAddress).To(Equal(req.BillingAddress))
			Expect(res.ShippingAddress).To(Equal(req.ShippingAddress))
			Expect(res.Gift).To(Equal(req.Gift))
			Expect(res.GiftMessage).To(Equal(normalize(req.GiftMessage)))
			Expect(res.GiftEmail).To(Equal(normalize(req.GiftEmail)))
		})
	})

	Context("Get order", func() {
		req := new(order.Order)
		res := new(order.Order)

		Before(func() {
			prod := product.Fake(db)
			prod.MustCreate()
			vari := variant.Fake(db, prod.Id())
			vari.MustCreate()
			li := lineitem.Fake(vari)
			req = order.Fake(db, li)
			req.MustCreate()

			res = order.New(db)

			cl.Get("/order/"+req.Id(), res)
		})

		It("Should get orders", func() {
			Expect(res.Email).To(Equal(normalize(req.Email)))
			Expect(res.Status).To(Equal(req.Status))
			Expect(res.PaymentStatus).To(Equal(req.PaymentStatus))
			Expect(res.Preorder).To(Equal(req.Preorder))
			Expect(res.Unconfirmed).To(Equal(req.Unconfirmed))
			Expect(res.Currency).To(Equal(req.Currency))
			//TODO: These are coming back as zero on the POST request, let's figure out why
			//Expect(res.LineTotal).To(Equal(req.LineTotal))
			//Expect(res.Discount).To(Equal(req.Discount))
			//Expect(res.Subtotal).To(Equal(req.Subtotal))
			//Expect(res.Shipping).To(Equal(req.Shipping))
			//Expect(res.Tax).To(Equal(req.Tax))
			//Expect(res.Total).To(Equal(req.Total))
			//TODO: Prices on the items are also coming back as zero.
			//Expect(res.Items).To(Equal(req.Items))
			Expect(res.Company).To(Equal(req.Company))
			Expect(res.Balance).To(Equal(req.Balance))
			Expect(res.Paid).To(Equal(req.Paid))
			Expect(res.Refunded).To(Equal(req.Refunded))
			Expect(res.Company).To(Equal(req.Company))
			Expect(res.BillingAddress).To(Equal(req.BillingAddress))
			Expect(res.ShippingAddress).To(Equal(req.ShippingAddress))
			Expect(res.Gift).To(Equal(req.Gift))
			Expect(res.GiftMessage).To(Equal(req.GiftMessage))
			Expect(res.GiftEmail).To(Equal(normalize(req.GiftEmail)))
		})
	})

	Context("Patch order", func() {
		ord := new(order.Order)
		res := new(order.Order)

		req := struct {
			Email   string `json:"email"`
			Company string `json:"company"`
		}{
			fake.EmailAddress(),
			fake.Company(),
		}

		Before(func() {
			prod := product.Fake(db)
			prod.MustCreate()
			vari := variant.Fake(db, prod.Id())
			vari.MustCreate()
			li := lineitem.Fake(vari)

			// Create order
			ord = order.Fake(db, li)
			ord.MustCreate()

			res = order.New(db)

			// Patch order
			cl.Patch("/order/"+ord.Id(), req, res)
		})

		It("Should patch order", func() {
			Expect(res.Id_).To(Equal(ord.Id()))
			Expect(normalize(res.Email)).To(Equal(normalize(req.Email)))
			Expect(res.Status).To(Equal(ord.Status))
			Expect(res.PaymentStatus).To(Equal(ord.PaymentStatus))
			Expect(res.Preorder).To(Equal(ord.Preorder))
			Expect(res.Unconfirmed).To(Equal(ord.Unconfirmed))
			Expect(res.Currency).To(Equal(ord.Currency))
			//TODO: These are coming back as zero on the POST orduest, let's figure out why
			//Expect(res.LineTotal).To(Equal(ord.LineTotal))
			//Expect(res.Discount).To(Equal(ord.Discount))
			//Expect(res.Subtotal).To(Equal(ord.Subtotal))
			//Expect(res.Shipping).To(Equal(ord.Shipping))
			//Expect(res.Tax).To(Equal(ord.Tax))
			//Expect(res.Total).To(Equal(ord.Total))
			//TODO: Prices on the items are also coming back as zero.
			//Expect(res.Items).To(Equal(ord.Items))
			Expect(res.Company).To(Equal(req.Company))
			Expect(res.Balance).To(Equal(ord.Balance))
			Expect(res.Paid).To(Equal(ord.Paid))
			Expect(res.Refunded).To(Equal(ord.Refunded))
			Expect(res.BillingAddress).To(Equal(ord.BillingAddress))
			Expect(res.ShippingAddress).To(Equal(ord.ShippingAddress))
			Expect(res.Gift).To(Equal(ord.Gift))
			Expect(res.GiftMessage).To(Equal(ord.GiftMessage))
			Expect(res.GiftEmail).To(Equal(normalize(ord.GiftEmail)))
		})
	})

	Context("Put order", func() {
		ord := new(order.Order)
		res := new(order.Order)
		req := new(order.Order)

		Before(func() {
			prod := product.Fake(db)
			prod.MustCreate()
			vari := variant.Fake(db, prod.Id())
			vari.MustCreate()
			li := lineitem.Fake(vari)

			// Create order
			ord = order.Fake(db, li)
			ord.MustCreate()

			req = order.Fake(db, li)
			res = order.New(db)

			// Update order
			cl.Put("/order/"+ord.Id(), req, res)
		})

		It("Should put order", func() {
			Expect(res.Id_).To(Equal(ord.Id()))
			Expect(normalize(res.Email)).To(Equal(normalize(req.Email)))
			Expect(res.Status).To(Equal(req.Status))
			Expect(res.PaymentStatus).To(Equal(req.PaymentStatus))
			Expect(res.Preorder).To(Equal(req.Preorder))
			Expect(res.Unconfirmed).To(Equal(req.Unconfirmed))
			Expect(res.Currency).To(Equal(req.Currency))
			//TODO: These are coming back as zero on the POST request, let's figure out why
			//Expect(res.LineTotal).To(Equal(req.LineTotal))
			//Expect(res.Discount).To(Equal(req.Discount))
			//Expect(res.Subtotal).To(Equal(req.Subtotal))
			//Expect(res.Shipping).To(Equal(req.Shipping))
			//Expect(res.Tax).To(Equal(req.Tax))
			//Expect(res.Total).To(Equal(req.Total))
			//TODO: Prices on the items are also coming back as zero.
			//Expect(res.Items).To(Equal(req.Items))
			Expect(res.Company).To(Equal(req.Company))
			Expect(res.Balance).To(Equal(req.Balance))
			Expect(res.Paid).To(Equal(req.Paid))
			Expect(res.Refunded).To(Equal(req.Refunded))
			Expect(res.Company).To(Equal(req.Company))
			Expect(res.BillingAddress).To(Equal(req.BillingAddress))
			Expect(res.ShippingAddress).To(Equal(req.ShippingAddress))
			Expect(res.Gift).To(Equal(req.Gift))
			Expect(res.GiftMessage).To(Equal(req.GiftMessage))
			Expect(res.GiftEmail).To(Equal(normalize(req.GiftEmail)))
		})
	})

	Context("Delete order", func() {
		res := ""

		Before(func() {
			prod := product.Fake(db)
			prod.MustCreate()
			vari := variant.Fake(db, prod.Id())
			vari.MustCreate()
			li := lineitem.Fake(vari)
			req := order.Fake(db, li)
			req.MustCreate()

			cl.Delete("/order/" + req.Id())

			res = req.Id()
		})

		It("Should delete orders", func() {
			ord := order.New(db)
			err := ord.GetById(res)
			Expect(err).ToNot(BeNil())
		})
	})
})
