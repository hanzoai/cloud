package test

import (
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var _ = Describe("thirdparty.authorizenet.payments", func() {

	Context("Payments", func() {
		It("Should succeed to authorize", func() {
			pay := payment.Fake(db)
			retPay, err := client.Authorize(pay)
			Expect(err).ToNot(HaveOccurred())
			Expect(retPay.Account.TransId).NotTo(BeNil())
			Expect(retPay.Account.TransId).NotTo(Equal(""))
		})
		It("Should succeed to charge an authorized payment", func() {
			pay := payment.Fake(db)
			retPay, err := client.Authorize(pay)
			Expect(err).ToNot(HaveOccurred())
			capPay, err := client.Capture(retPay)
			Expect(capPay.Captured).To(Equal(true))
			Expect(err).ToNot(HaveOccurred())
			Expect(capPay.Account.TransId).NotTo(BeNil())
			Expect(capPay.Account.TransId).NotTo(Equal(""))
		})
		It("Should succeed a one-step charge", func() {
			pay := payment.Fake(db)
			chrgPay, err := client.Charge(pay)
			Expect(chrgPay.Captured).To(Equal(true))
			Expect(err).ToNot(HaveOccurred())
			Expect(chrgPay.Account.TransId).NotTo(BeNil())
			Expect(chrgPay.Account.TransId).NotTo(Equal(""))
		})
		It("Should succeed at a refund", func() {
			pay := payment.Fake(db)
			chrgPay, err := client.Charge(pay)
			Expect(err).ToNot(HaveOccurred())
			Expect(chrgPay.Account.TransId).NotTo(BeNil())
			Expect(chrgPay.Account.TransId).NotTo(Equal(""))
			_, err = client.RefundPayment(pay, currency.Cents(50))
			Expect(err.Error()).To(Equal("Unable to refund unpaid transaction"))
			// AUthorize.net only allows settled transactions to be refunded.
			// That usually means the next day.
			// We can't test the happy path, but we can ensure that the API
			// At least understood our request.
		})

		It("Should authorize a simple payment", func() {
			pay := payment.New(db)
			pay.Amount = 2000
			pay.Account.Name = "Test"
			pay.Account.CVC = "424"
			pay.Account.Month = 5
			pay.Account.Year = 2025
			pay.Account.Number = "4242424242424242"

			retPay, err := client.Authorize(pay)

			Expect(err).ToNot(HaveOccurred())
			capPay, err := client.Capture(retPay)
			Expect(capPay.Captured).To(Equal(true))
			Expect(err).ToNot(HaveOccurred())
			Expect(capPay.Account.TransId).NotTo(BeNil())
			Expect(capPay.Account.TransId).NotTo(Equal(""))
		})
	})
})
