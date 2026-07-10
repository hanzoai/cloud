package test

import (
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	//"github.com/hanzoai/cloud/clients/commerce/thirdparty/authorizenet"
	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var _ = Describe("thirdparty.authorizenet.subscription", func() {
	Context("Subscription", func() {
		It("Should make new subscription", func() {
			sub := order.FakeSubscription(db)
			sub.Price += currency.Cents(1) // a.net thinks a subscription worth nothing makes no sense.
			retSub, err := client.NewSubscription(sub)
			Expect(err).ToNot(HaveOccurred())
			Expect(retSub.Account.TransId).NotTo(BeNil())
			Expect(retSub.Ref.AuthorizeNet.SubscriptionId).NotTo(Equal(""))
			Expect(retSub.Ref.AuthorizeNet.CustomerProfileId).NotTo(Equal(""))
			Expect(retSub.Ref.AuthorizeNet.CustomerPaymentProfileId).NotTo(Equal(""))
		})
		It("Should cancel subscription", func() {
			sub := order.FakeSubscription(db)
			sub.Price += currency.Cents(1) // a.net thinks a subscription worth nothing makes no sense.
			retSub, err := client.NewSubscription(sub)
			Expect(err).ToNot(HaveOccurred())
			canceledSub, err := client.CancelSubscription(retSub)
			Expect(err).ToNot(HaveOccurred())
			Expect(canceledSub.Canceled).To(BeTrue())
		})
		It("Should update subscription", func() {
			sub := order.FakeSubscription(db)
			sub.Price += currency.Cents(1) // a.net thinks a subscription worth nothing makes no sense.
			retSub, err := client.NewSubscription(sub)
			Expect(err).ToNot(HaveOccurred())
			retSub.Account.Number = "5555555555554444"
			retSub.Account.CVC = "434"
			retSub.Account.Month = 12
			retSub.Account.Year = 2025
			updateSub, err := client.UpdateSubscription(retSub)
			log.JSON(updateSub)
			Expect(updateSub.Ref.AuthorizeNet.SubscriptionId).NotTo(Equal(""))
			Expect(updateSub.Ref.AuthorizeNet.CustomerProfileId).NotTo(Equal(""))
			Expect(updateSub.Ref.AuthorizeNet.CustomerPaymentProfileId).NotTo(Equal(""))
		})
	})
})
