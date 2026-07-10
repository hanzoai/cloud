package test

import (
	"math/big"

	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/payment"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"

	. "github.com/hanzoai/cloud/clients/commerce/thirdparty/ethereum/tasks"
	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func MulInts(a, b int64) *big.Int {
	return big.NewInt(a).Mul(big.NewInt(a), big.NewInt(b))
}

func MulFraction(a *big.Int, b, c int64) *big.Int {
	d := NewInt().Mul(a, big.NewInt(b))
	d.Div(d, big.NewInt(c))
	return d
}

func CloneInt(a *big.Int) *big.Int {
	return NewInt().Set(a)
}

func NewInt() *big.Int {
	return big.NewInt(0)
}

var _ = Describe("thirdparty/ethereum/tasks/pay.go", func() {
	Context("tasks.EthereumProcessPayment", func() {
		var totalCents = currency.Cents(123e6)
		var totalGas = big.NewInt(21000)

		Before(func() {
			ord.Paid = 0
			ord.PaymentStatus = payment.Unpaid
			ord.PaymentIds = []string{}
			ord.MustUpdate()
		})

		It("Should Create a Payment", func() {
			totalInt1 := MulInts(123e6, 1e9)

			txHash := "testHash123"
			chainType := blockchains.EthereumRopstenType
			err := EthereumProcessPaymentImpl(
				ctx,
				"suchtees",
				w.Id(),
				txHash,
				"123",
				"456",
				string(chainType),
				totalInt1,
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
			ok, err = pay2.Query().Filter("Account.EthereumTransactionHash=", txHash).Get()
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(ord2.PaymentIds[0]).To(Equal(pay2.Id()))

			Expect(pay2.Account.EthereumTransactionHash).To(Equal(txHash))
			Expect(pay2.Account.EthereumChainType).To(Equal(chainType))
			Expect(pay2.Account.EthereumAmount).To(Equal(blockchains.BigNumber(totalInt1.String())))

			Expect(pay2.Account.EthereumFromAddress).To(Equal("123"))
			Expect(pay2.Account.EthereumToAddress).To(Equal("456"))
			Expect(pay2.Account.EthereumFinalTransactionCost).To(Equal(blockchains.BigNumber(totalGas.String())))
			Expect(pay2.Account.EthereumFinalAddress).To(Equal("0xf2fccc0198fc6b39246bd91272769d46d2f9d43b"))
			Expect(pay2.Account.EthereumFinalAmount).To(Equal(blockchains.BigNumber(NewInt().Sub(MulFraction(totalInt1, 19, 20), totalGas).String())))

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

			Expect(fe.Ethereum.FinalTransactionHash).To(Equal("0x0"))
			Expect(fe.Ethereum.FinalTransactionCost).To(Equal(blockchains.BigNumber(totalGas.String())))
			Expect(fe.Ethereum.FinalAddress).To(Equal(pw.Accounts[0].Address))
			Expect(fe.Ethereum.FinalAmount).To(Equal(blockchains.BigNumber(NewInt().Sub(MulFraction(totalInt1, 1, 20), totalGas).String())))
		})

		It("Should Create a Multiple Payments Overpayment", func() {
			totalInt1 := MulInts(123e6, 1e9)
			totalInt2 := MulInts(321e6, 1e9)

			txHash1 := "testHash123o"
			txHash2 := "testHash321o"
			chainType := blockchains.EthereumRopstenType
			err := EthereumProcessPaymentImpl(
				ctx,
				"suchtees",
				w.Id(),
				txHash1,
				"123",
				"456",
				string(chainType),
				totalInt1,
			)

			Expect(err).ToNot(HaveOccurred())

			err = EthereumProcessPaymentImpl(
				ctx,
				"suchtees",
				w.Id(),
				txHash2,
				"123",
				"456",
				string(chainType),
				totalInt2,
			)

			Expect(err).ToNot(HaveOccurred())

			ord2 := order.New(nsDb)
			ok, err := ord2.Query().Filter("WalletId=", w.Id()).Get()
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())

			Expect(ord2.Paid).To(Equal(currency.Cents(444e6)))
			Expect(ord2.PaymentStatus).To(Equal(payment.Paid))
			Expect(len(ord2.PaymentIds)).To(Equal(2))

			pay1 := payment.New(nsDb)
			ok, err = pay1.Query().Filter("Account.EthereumTransactionHash=", txHash1).Get()
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(ord2.PaymentIds[0]).To(Equal(pay1.Id()))

			Expect(pay1.Account.EthereumTransactionHash).To(Equal(txHash1))
			Expect(pay1.Account.EthereumChainType).To(Equal(chainType))
			Expect(pay1.Account.EthereumAmount).To(Equal(blockchains.BigNumber(totalInt1.String())))

			Expect(pay1.Account.EthereumFromAddress).To(Equal("123"))
			Expect(pay1.Account.EthereumToAddress).To(Equal("456"))
			Expect(pay1.Account.EthereumFinalTransactionHash).To(Equal("0x0"))
			Expect(pay1.Account.EthereumFinalTransactionCost).To(Equal(blockchains.BigNumber(totalGas.String())))
			Expect(pay1.Account.EthereumFinalAddress).To(Equal("0xf2fccc0198fc6b39246bd91272769d46d2f9d43b"))
			Expect(pay1.Account.EthereumFinalAmount).To(Equal(blockchains.BigNumber(NewInt().Sub(MulFraction(totalInt1, 19, 20), totalGas).String())))

			Expect(pay1.Fee).To(Equal(currency.Cents(.05 * 123e6)))
			Expect(len(pay1.FeeIds)).To(Equal(1))

			fees1, err := pay1.GetFees()
			Expect(err).ToNot(HaveOccurred())

			fe1 := fees1[0]
			Expect(fe1.Currency).To(Equal(currency.ETH))
			Expect(fe1.Amount).To(Equal(currency.Cents(.05 * 123e6)))

			Expect(fe1.Ethereum.FinalTransactionHash).To(Equal("0x0"))
			Expect(fe1.Ethereum.FinalTransactionCost).To(Equal(blockchains.BigNumber(totalGas.String())))
			Expect(fe1.Ethereum.FinalAddress).To(Equal(pw.Accounts[0].Address))
			Expect(fe1.Ethereum.FinalAmount).To(Equal(blockchains.BigNumber(NewInt().Sub(MulFraction(totalInt1, 1, 20), totalGas).String())))

			Expect(pay1.Test).To(BeTrue())
			Expect(pay1.Status).To(Equal(payment.Paid))
			Expect(pay1.Type).To(Equal(ord.Type))
			Expect(pay1.Buyer).To(Equal(usr.Buyer()))
			Expect(pay1.Currency).To(Equal(ord.Currency))
			Expect(pay1.OrderId).To(Equal(ord.Id()))
			Expect(pay1.UserId).To(Equal(usr.Id()))
			Expect(pay1.Amount).To(Equal(currency.Cents(123e6)))

			pay2 := payment.New(nsDb)
			ok, err = pay2.Query().Filter("Account.EthereumTransactionHash=", txHash2).Get()
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(ord2.PaymentIds[1]).To(Equal(pay2.Id()))

			Expect(pay2.Account.EthereumTransactionHash).To(Equal(txHash2))
			Expect(pay2.Account.EthereumChainType).To(Equal(chainType))
			Expect(pay2.Account.EthereumAmount).To(Equal(blockchains.BigNumber(totalInt2.String())))

			Expect(pay2.Account.EthereumFromAddress).To(Equal("123"))
			Expect(pay2.Account.EthereumToAddress).To(Equal("456"))
			Expect(pay2.Account.EthereumFinalTransactionHash).To(Equal(""))
			Expect(pay2.Account.EthereumFinalTransactionCost).To(Equal(blockchains.BigNumber("")))
			Expect(pay2.Account.EthereumFinalAddress).To(Equal(""))
			Expect(pay2.Account.EthereumFinalAmount).To(Equal(blockchains.BigNumber("")))

			Expect(pay2.Test).To(BeTrue())
			Expect(pay2.Status).To(Equal(payment.Paid))
			Expect(pay2.Type).To(Equal(ord.Type))
			Expect(pay2.Buyer).To(Equal(usr.Buyer()))
			Expect(pay2.Currency).To(Equal(ord.Currency))
			Expect(pay2.OrderId).To(Equal(ord.Id()))
			Expect(pay2.UserId).To(Equal(usr.Id()))
			Expect(pay2.Amount).To(Equal(currency.Cents(321e6)))

			Expect(pay2.Fee).To(Equal(currency.Cents(0)))
			Expect(len(pay2.FeeIds)).To(Equal(0))
		})

		It("Should Create a Multiple Payments Underpayment", func() {
			totalInt1 := MulInts(100e6, 1e9)
			totalInt2 := MulInts(23e6, 1e9)
			totalInts := MulInts(123e6, 1e9)

			txHash1 := "testHash100u"
			txHash2 := "testHash023u"
			chainType := blockchains.EthereumRopstenType
			err := EthereumProcessPaymentImpl(
				ctx,
				"suchtees",
				w.Id(),
				txHash1,
				"123",
				"456",
				string(chainType),
				totalInt1,
			)

			Expect(err).ToNot(HaveOccurred())

			err = EthereumProcessPaymentImpl(
				ctx,
				"suchtees",
				w.Id(),
				txHash2,
				"123",
				"456",
				string(chainType),
				totalInt2,
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
			ok, err = pay1.Query().Filter("Account.EthereumTransactionHash=", txHash1).Get()
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(ord2.PaymentIds[0]).To(Equal(pay1.Id()))

			Expect(pay1.Account.EthereumTransactionHash).To(Equal(txHash1))
			Expect(pay1.Account.EthereumChainType).To(Equal(chainType))
			Expect(pay1.Account.EthereumAmount).To(Equal(blockchains.BigNumber(totalInt1.String())))

			Expect(pay1.Account.EthereumFromAddress).To(Equal("123"))
			Expect(pay1.Account.EthereumToAddress).To(Equal("456"))
			Expect(pay1.Account.EthereumFinalTransactionHash).To(Equal(""))
			Expect(pay1.Account.EthereumFinalTransactionCost).To(Equal(blockchains.BigNumber("")))
			Expect(pay1.Account.EthereumFinalAddress).To(Equal(""))
			Expect(pay1.Account.EthereumFinalAmount).To(Equal(blockchains.BigNumber("")))

			Expect(pay1.Test).To(BeTrue())
			Expect(pay1.Status).To(Equal(payment.Paid))
			Expect(pay1.Type).To(Equal(ord.Type))
			Expect(pay1.Buyer).To(Equal(usr.Buyer()))
			Expect(pay1.Currency).To(Equal(ord.Currency))
			Expect(pay1.OrderId).To(Equal(ord.Id()))
			Expect(pay1.UserId).To(Equal(usr.Id()))
			Expect(pay1.Amount).To(Equal(currency.Cents(100e6)))

			Expect(pay1.Fee).To(Equal(currency.Cents(0)))
			Expect(len(pay1.FeeIds)).To(Equal(0))

			pay2 := payment.New(nsDb)
			ok, err = pay2.Query().Filter("Account.EthereumTransactionHash=", txHash2).Get()
			Expect(err).ToNot(HaveOccurred())
			Expect(ok).To(BeTrue())
			Expect(ord2.PaymentIds[1]).To(Equal(pay2.Id()))

			Expect(pay2.Account.EthereumTransactionHash).To(Equal(txHash2))
			Expect(pay2.Account.EthereumChainType).To(Equal(chainType))
			Expect(pay2.Account.EthereumAmount).To(Equal(blockchains.BigNumber(totalInt2.String())))

			Expect(pay2.Account.EthereumFromAddress).To(Equal("123"))
			Expect(pay2.Account.EthereumToAddress).To(Equal("456"))
			Expect(pay2.Account.EthereumFinalTransactionHash).To(Equal("0x0"))
			Expect(pay2.Account.EthereumFinalTransactionCost).To(Equal(blockchains.BigNumber(totalGas.String())))
			Expect(pay2.Account.EthereumFinalAddress).To(Equal("0xf2fccc0198fc6b39246bd91272769d46d2f9d43b"))
			Expect(pay2.Account.EthereumFinalAmount).To(Equal(blockchains.BigNumber(NewInt().Sub(MulFraction(totalInts, 19, 20), totalGas).String())))

			Expect(pay2.Fee).To(Equal(currency.Cents(.05 * 123e6)))
			Expect(len(pay2.FeeIds)).To(Equal(1))

			fees1, err := pay2.GetFees()
			Expect(err).ToNot(HaveOccurred())

			fe2 := fees1[0]
			Expect(fe2.Currency).To(Equal(currency.ETH))
			Expect(fe2.Amount).To(Equal(currency.Cents(.05 * 123e6)))

			Expect(fe2.Ethereum.FinalTransactionHash).To(Equal("0x0"))
			Expect(fe2.Ethereum.FinalTransactionCost).To(Equal(blockchains.BigNumber(totalGas.String())))
			Expect(fe2.Ethereum.FinalAddress).To(Equal(pw.Accounts[0].Address))
			Expect(fe2.Ethereum.FinalAmount).To(Equal(blockchains.BigNumber(NewInt().Sub(MulFraction(totalInts, 1, 20), totalGas).String())))

			Expect(pay2.Test).To(BeTrue())
			Expect(pay2.Status).To(Equal(payment.Paid))
			Expect(pay2.Type).To(Equal(ord.Type))
			Expect(pay2.Buyer).To(Equal(usr.Buyer()))
			Expect(pay2.Currency).To(Equal(ord.Currency))
			Expect(pay2.OrderId).To(Equal(ord.Id()))
			Expect(pay2.UserId).To(Equal(usr.Id()))
			Expect(pay2.Amount).To(Equal(currency.Cents(23e6)))
		})

		It("Should Ignore Duplicates", func() {
			txId := "testHash123d"
			totalInt1 := MulInts(100e6, 1e9)

			chainType := blockchains.EthereumRopstenType
			err := EthereumProcessPaymentImpl(
				ctx,
				"suchtees",
				w.Id(),
				txId,
				"123",
				"456",
				string(chainType),
				big.NewInt(1e9),
			)

			Expect(err).ToNot(HaveOccurred())

			err = EthereumProcessPaymentImpl(
				ctx,
				"suchtees",
				w.Id(),
				txId,
				"123",
				"456",
				string(chainType),
				totalInt1,
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
