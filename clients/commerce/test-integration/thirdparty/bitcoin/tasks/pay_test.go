package test

import (
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/bitcoin"

	. "github.com/hanzoai/cloud/clients/commerce/thirdparty/bitcoin/tasks"
	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var _ = Describe("thirdparty/bitcoin/tasks/pay.go", func() {
	Context("tasks.BitcoinProcessPayment", func() {
		var totalCents = currency.Cents(123e6)

		Before(func() {
			ord.Paid = 0
			ord.PaymentStatus = payment.Unpaid
			ord.PaymentIds = []string{}
			ord.MustUpdate()
		})

		It("Should Create a Payment", func() {
			totalInt1 := totalCents
			totalCost := currency.Cents(bitcoin.CalculateFee(1, 2, 0))
			txId := "testHash123"

			chainType := blockchains.BitcoinTestnetType
			err := BitcoinProcessPaymentImpl(
				ctx,
				"suchtees",
				w.Id(),
				txId,
				string(chainType),
				int64(totalInt1),
			)

			Expect(err).ToNot(HaveOccurred())

			ord2 := order.New(nsDb)
			ok, err := ord2.Query().Filter("WalletId=", w.Id()).Get()
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())

			Expect(ord2.Paid).To(Equal(totalCents))
			Expect(ord2.PaymentStatus).To(Equal(payment.Paid))
			Expect(len(ord2.PaymentIds)).To(Equal(1))

			pay2 := payment.New(nsDb)
			ok, err = pay2.Query().Filter("Account.BitcoinTransactionTxId=", txId).Get()
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(ord2.PaymentIds[0]).To(Equal(pay2.Id()))

			Expect(pay2.Account.BitcoinTransactionTxId).To(Equal(txId))
			Expect(pay2.Account.BitcoinChainType).To(Equal(chainType))
			Expect(pay2.Account.BitcoinAmount).To(Equal(totalInt1))

			Expect(pay2.Account.BitcoinFinalTransactionTxId).To(Equal("0"))
			Expect(pay2.Account.BitcoinFinalTransactionCost).To(Equal(totalCost))
			Expect(pay2.Account.BitcoinFinalAddress).To(Equal("mrPFGX5ViUZk2s8i5soBCkrFVzRwngK8DQ"))
			Expect(pay2.Account.BitcoinFinalAmount).To(Equal(currency.Cents(float64(totalInt1)*.95 - float64(totalCost))))

			Expect(pay2.Test).To(BeTrue())
			Expect(pay2.Status).To(Equal(payment.Paid))
			Expect(pay2.Type).To(Equal(ord.Type))
			Expect(pay2.Buyer).To(Equal(usr.Buyer()))
			Expect(pay2.Currency).To(Equal(ord.Currency))
			Expect(pay2.OrderId).To(Equal(ord.Id()))
			Expect(pay2.UserId).To(Equal(usr.Id()))
			Expect(pay2.Amount).To(Equal(totalCents))

			Expect(pay2.Fee).To(Equal(currency.Cents(.05 * 123e6)))
			Expect(len(pay2.FeeIds)).To(Equal(1))

			fees, err := pay2.GetFees()
			Expect(err).ToNot(HaveOccurred())

			fe := fees[0]
			Expect(fe.Currency).To(Equal(currency.ETH))
			Expect(fe.Amount).To(Equal(currency.Cents(.05 * 123e6)))

			Expect(fe.Bitcoin.FinalTransactionTxId).To(Equal("0"))
			Expect(fe.Bitcoin.FinalAddress).To(Equal(pw.Accounts[2].Address))
			Expect(fe.Bitcoin.FinalAmount).To(Equal(currency.Cents(float64(totalInt1) * .05)))
			Expect(fe.Bitcoin.FinalVOut).To(Equal(int64(0)))
		})

		It("Should Create a Multiple Payments Overpayment", func() {
			txId1 := "testHash123o"
			txId2 := "testHash321o"
			totalInt1 := currency.Cents(123e6)
			totalInt2 := currency.Cents(321e6)
			totalInt3 := totalInt1 + totalInt2
			totalCost := currency.Cents(bitcoin.CalculateFee(1, 2, 0))

			chainType := blockchains.BitcoinTestnetType
			err := BitcoinProcessPaymentImpl(
				ctx,
				"suchtees",
				w.Id(),
				txId1,
				string(chainType),
				int64(totalInt1),
			)

			Expect(err).ToNot(HaveOccurred())

			err = BitcoinProcessPaymentImpl(
				ctx,
				"suchtees",
				w.Id(),
				txId2,
				string(chainType),
				int64(totalInt2),
			)

			Expect(err).ToNot(HaveOccurred())

			ord2 := order.New(nsDb)
			ok, err := ord2.Query().Filter("WalletId=", w.Id()).Get()
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())

			Expect(ord2.Paid).To(Equal(totalInt3))
			Expect(ord2.PaymentStatus).To(Equal(payment.Paid))
			Expect(len(ord2.PaymentIds)).To(Equal(2))

			pay1 := payment.New(nsDb)
			ok, err = pay1.Query().Filter("Account.BitcoinTransactionTxId=", txId1).Get()
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(ord2.PaymentIds[0]).To(Equal(pay1.Id()))

			Expect(pay1.Account.BitcoinTransactionTxId).To(Equal(txId1))
			Expect(pay1.Account.BitcoinChainType).To(Equal(chainType))
			Expect(pay1.Account.BitcoinAmount).To(Equal(totalInt1))

			Expect(pay1.Account.BitcoinFinalTransactionTxId).To(Equal("0"))
			Expect(pay1.Account.BitcoinFinalTransactionCost).To(Equal(totalCost))
			Expect(pay1.Account.BitcoinFinalAddress).To(Equal("mrPFGX5ViUZk2s8i5soBCkrFVzRwngK8DQ"))
			Expect(pay1.Account.BitcoinFinalAmount).To(Equal(currency.Cents(float64(totalCents)*.95 - float64(totalCost))))

			Expect(pay1.Fee).To(Equal(currency.Cents(.05 * 123e6)))
			Expect(len(pay1.FeeIds)).To(Equal(1))

			fees1, err := pay1.GetFees()
			Expect(err).ToNot(HaveOccurred())

			fe1 := fees1[0]
			Expect(fe1.Currency).To(Equal(currency.ETH))
			Expect(fe1.Amount).To(Equal(currency.Cents(.05 * 123e6)))

			Expect(fe1.Bitcoin.FinalTransactionTxId).To(Equal("0"))
			Expect(fe1.Bitcoin.FinalAddress).To(Equal(pw.Accounts[2].Address))
			Expect(fe1.Bitcoin.FinalAmount).To(Equal(currency.Cents(float64(totalCents) * .05)))
			Expect(fe1.Bitcoin.FinalVOut).To(Equal(int64(0)))

			Expect(pay1.Test).To(BeTrue())
			Expect(pay1.Status).To(Equal(payment.Paid))
			Expect(pay1.Type).To(Equal(ord.Type))
			Expect(pay1.Buyer).To(Equal(usr.Buyer()))
			Expect(pay1.Currency).To(Equal(ord.Currency))
			Expect(pay1.OrderId).To(Equal(ord.Id()))
			Expect(pay1.UserId).To(Equal(usr.Id()))
			Expect(pay1.Amount).To(Equal(totalInt1))

			pay2 := payment.New(nsDb)
			ok, err = pay2.Query().Filter("Account.BitcoinTransactionTxId=", txId2).Get()
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(ord2.PaymentIds[1]).To(Equal(pay2.Id()))

			Expect(pay2.Account.BitcoinTransactionTxId).To(Equal(txId2))
			Expect(pay2.Account.BitcoinChainType).To(Equal(chainType))
			Expect(pay2.Account.BitcoinAmount).To(Equal(totalInt2))

			Expect(pay2.Account.BitcoinFinalTransactionTxId).To(Equal(""))
			Expect(pay2.Account.BitcoinFinalTransactionCost).To(Equal(currency.Cents(0)))
			Expect(pay2.Account.BitcoinFinalAddress).To(Equal(""))
			Expect(pay2.Account.BitcoinFinalAmount).To(Equal(currency.Cents(0)))

			Expect(pay2.Test).To(BeTrue())
			Expect(pay2.Status).To(Equal(payment.Paid))
			Expect(pay2.Type).To(Equal(ord.Type))
			Expect(pay2.Buyer).To(Equal(usr.Buyer()))
			Expect(pay2.Currency).To(Equal(ord.Currency))
			Expect(pay2.OrderId).To(Equal(ord.Id()))
			Expect(pay2.UserId).To(Equal(usr.Id()))
			Expect(pay2.Amount).To(Equal(totalInt2))

			Expect(pay2.Fee).To(Equal(currency.Cents(0)))
			Expect(len(pay2.FeeIds)).To(Equal(0))
		})

		It("Should Create a Multiple Payments Underpayment", func() {
			txId1 := "testHash100u"
			txId2 := "testHash023u"
			totalInt1 := currency.Cents(100e6)
			totalInt2 := currency.Cents(23e6)
			totalCost := currency.Cents(bitcoin.CalculateFee(1, 2, 0))

			chainType := blockchains.BitcoinTestnetType
			err := BitcoinProcessPaymentImpl(
				ctx,
				"suchtees",
				w.Id(),
				txId1,
				string(chainType),
				int64(totalInt1),
			)

			Expect(err).ToNot(HaveOccurred())

			err = BitcoinProcessPaymentImpl(
				ctx,
				"suchtees",
				w.Id(),
				txId2,
				string(chainType),
				int64(totalInt2),
			)

			Expect(err).ToNot(HaveOccurred())

			ord2 := order.New(nsDb)
			ok, err := ord2.Query().Filter("WalletId=", w.Id()).Get()
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())

			Expect(ord2.Paid).To(Equal(totalCents))
			Expect(ord2.PaymentStatus).To(Equal(payment.Paid))
			Expect(len(ord2.PaymentIds)).To(Equal(2))

			pay1 := payment.New(nsDb)
			ok, err = pay1.Query().Filter("Account.BitcoinTransactionTxId=", txId1).Get()
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(ord2.PaymentIds[0]).To(Equal(pay1.Id()))

			Expect(pay1.Account.BitcoinTransactionTxId).To(Equal(txId1))
			Expect(pay1.Account.BitcoinChainType).To(Equal(chainType))
			Expect(pay1.Account.BitcoinAmount).To(Equal(totalInt1))

			Expect(pay1.Account.BitcoinFinalTransactionTxId).To(Equal(""))
			Expect(pay1.Account.BitcoinFinalTransactionCost).To(Equal(currency.Cents(0)))
			Expect(pay1.Account.BitcoinFinalAddress).To(Equal(""))
			Expect(pay1.Account.BitcoinFinalAmount).To(Equal(currency.Cents(0)))

			Expect(pay1.Fee).To(Equal(currency.Cents(0)))
			Expect(len(pay1.FeeIds)).To(Equal(0))

			Expect(pay1.Test).To(BeTrue())
			Expect(pay1.Status).To(Equal(payment.Paid))
			Expect(pay1.Type).To(Equal(ord.Type))
			Expect(pay1.Buyer).To(Equal(usr.Buyer()))
			Expect(pay1.Currency).To(Equal(ord.Currency))
			Expect(pay1.OrderId).To(Equal(ord.Id()))
			Expect(pay1.UserId).To(Equal(usr.Id()))
			Expect(pay1.Amount).To(Equal(totalInt1))

			pay2 := payment.New(nsDb)
			ok, err = pay2.Query().Filter("Account.BitcoinTransactionTxId=", txId2).Get()
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(ord2.PaymentIds[1]).To(Equal(pay2.Id()))

			Expect(pay2.Account.BitcoinTransactionTxId).To(Equal(txId2))
			Expect(pay2.Account.BitcoinChainType).To(Equal(chainType))
			Expect(pay2.Account.BitcoinAmount).To(Equal(totalInt2))

			Expect(pay2.Account.BitcoinFinalTransactionTxId).To(Equal("0"))
			Expect(pay2.Account.BitcoinFinalTransactionCost).To(Equal(totalCost))
			Expect(pay2.Account.BitcoinFinalAddress).To(Equal("mrPFGX5ViUZk2s8i5soBCkrFVzRwngK8DQ"))
			Expect(pay2.Account.BitcoinFinalAmount).To(Equal(currency.Cents(float64(totalCents)*.95 - float64(totalCost))))

			Expect(pay2.Test).To(BeTrue())
			Expect(pay2.Status).To(Equal(payment.Paid))
			Expect(pay2.Type).To(Equal(ord.Type))
			Expect(pay2.Buyer).To(Equal(usr.Buyer()))
			Expect(pay2.Currency).To(Equal(ord.Currency))
			Expect(pay2.OrderId).To(Equal(ord.Id()))
			Expect(pay2.UserId).To(Equal(usr.Id()))
			Expect(pay2.Amount).To(Equal(totalInt2))

			Expect(pay2.Fee).To(Equal(currency.Cents(.05 * 123e6)))
			Expect(len(pay2.FeeIds)).To(Equal(1))

			fees2, err := pay2.GetFees()
			Expect(err).ToNot(HaveOccurred())

			fe2 := fees2[0]
			Expect(fe2.Currency).To(Equal(currency.ETH))
			Expect(fe2.Amount).To(Equal(currency.Cents(.05 * 123e6)))

			Expect(fe2.Bitcoin.FinalTransactionTxId).To(Equal("0"))
			Expect(fe2.Bitcoin.FinalAddress).To(Equal(pw.Accounts[2].Address))
			Expect(fe2.Bitcoin.FinalAmount).To(Equal(currency.Cents(float64(totalCents) * .05)))
			Expect(fe2.Bitcoin.FinalVOut).To(Equal(int64(0)))
		})

		It("Should Ignore Duplicates", func() {
			txId := "testHash123d"
			totalInt1 := currency.Cents(123e6)

			chainType := blockchains.BitcoinTestnetType
			err := BitcoinProcessPaymentImpl(
				ctx,
				"suchtees",
				w.Id(),
				txId,
				string(chainType),
				int64(1),
			)

			Expect(err).ToNot(HaveOccurred())

			err = BitcoinProcessPaymentImpl(
				ctx,
				"suchtees",
				w.Id(),
				txId,
				string(chainType),
				int64(totalInt1),
			)

			Expect(err).ToNot(HaveOccurred())

			ord2 := order.New(nsDb)
			ok, err := ord2.Query().Filter("WalletId=", w.Id()).Get()
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())

			Expect(ord2.Paid).To(Equal(currency.Cents(1)))
			Expect(ord2.PaymentStatus).To(Equal(payment.Unpaid))
			Expect(len(ord2.PaymentIds)).To(Equal(1))
		})
	})
})
