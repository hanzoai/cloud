package test

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var (
	ctx  ae.Context
	db   *datastore.Datastore
	id   string
	kind string
)

var _ = BeforeSuite(func() {
	ctx = ae.NewContext()
	db = datastore.New(ctx)

	id = "12345"
	kind = "abcde"

	// Deposit
	tr1 := transaction.New(db)
	tr1.DestinationId = id
	tr1.DestinationKind = kind
	tr1.Currency = currency.USD
	tr1.Amount = currency.Cents(100)
	tr1.Type = transaction.Deposit
	tr1.MustCreate()

	// Withdraw
	tr2 := transaction.New(db)
	tr2.DestinationId = id
	tr2.DestinationKind = kind
	tr2.Currency = currency.USD
	tr2.Amount = currency.Cents(50)
	tr2.Type = transaction.Withdraw
	tr2.MustCreate()

	// Transfer To
	tr3 := transaction.New(db)
	tr3.DestinationId = id
	tr3.DestinationKind = kind
	tr3.Currency = currency.USD
	tr3.Amount = currency.Cents(20)
	tr3.Type = transaction.Transfer
	tr3.SourceId = "54321"
	tr3.SourceKind = kind
	tr3.MustCreate()

	// Transfer From
	tr4 := transaction.New(db)
	tr4.DestinationId = "54321"
	tr4.DestinationKind = kind
	tr4.Currency = currency.USD
	tr4.Amount = currency.Cents(10)
	tr4.Type = transaction.Transfer
	tr4.SourceId = id
	tr4.SourceKind = kind
	tr4.MustCreate()

	// Different Currency
	tr5 := transaction.New(db)
	tr5.DestinationId = id
	tr5.DestinationKind = kind
	tr5.Currency = currency.BTC
	tr5.Amount = currency.Cents(100)
	tr5.Type = transaction.Deposit
	tr5.MustCreate()

	// Different Id
	tr6 := transaction.New(db)
	tr6.DestinationId = id + "6"
	tr6.DestinationKind = kind
	tr6.Currency = currency.USD
	tr6.Amount = currency.Cents(100)
	tr6.Type = transaction.Deposit
	tr6.MustCreate()

	// Different Kind
	tr7 := transaction.New(db)
	tr7.DestinationId = id
	tr7.DestinationKind = kind + "f"
	tr7.Currency = currency.USD
	tr7.Amount = currency.Cents(100)
	tr7.Type = transaction.Deposit
	tr7.MustCreate()

	// Circular
	tr8 := transaction.New(db)
	tr8.DestinationId = id
	tr8.DestinationKind = kind
	tr8.Currency = currency.USD
	tr8.Amount = currency.Cents(10)
	tr8.Type = transaction.Transfer
	tr8.SourceId = id
	tr8.SourceKind = kind
	tr8.MustCreate()

	// Hold
	tr9 := transaction.New(db)
	tr9.Currency = currency.USD
	tr9.Amount = currency.Cents(10)
	tr9.Type = transaction.Hold
	tr9.SourceId = id
	tr9.SourceKind = kind
	tr9.MustCreate()
})

func Test(t *testing.T) {
	Setup("models/transaction/util", t)
}

var _ = Describe("util", func() {
	Context("GetTransactions", func() {
		It("Should work", func() {
			datas, err := util.GetTransactions(ctx, id, kind, false)
			Expect(err).NotTo(HaveOccurred())

			Expect(datas.Id).To(Equal(id))
			Expect(datas.Kind).To(Equal(kind))
			Expect(datas.Data[currency.USD] != nil).To(Equal(true))
			Expect(datas.Data[currency.BTC] != nil).To(Equal(true))

			Expect(len(datas.Data[currency.USD].Transactions)).To(Equal(5))
			Expect(datas.Data[currency.USD].Balance).To(Equal(currency.Cents(60)))
			Expect(datas.Data[currency.USD].Holds).To(Equal(currency.Cents(10)))
		})
	})

	Context("GetTransactionsByCurrency", func() {
		It("Should work", func() {
			datas, err := util.GetTransactionsByCurrency(ctx, id, kind, currency.USD, false)
			Expect(err).NotTo(HaveOccurred())

			Expect(datas.Id).To(Equal(id))
			Expect(datas.Kind).To(Equal(kind))
			Expect(datas.Data[currency.USD] != nil).To(Equal(true))
			Expect(datas.Data[currency.BTC] == nil).To(Equal(true))

			Expect(len(datas.Data[currency.USD].Transactions)).To(Equal(5))
			Expect(datas.Data[currency.USD].Balance).To(Equal(currency.Cents(60)))
			Expect(datas.Data[currency.USD].Holds).To(Equal(currency.Cents(10)))
		})
	})
})
