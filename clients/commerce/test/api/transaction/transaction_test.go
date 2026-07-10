package test

import (
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction/util"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	// "github.com/hanzoai/cloud/clients/commerce/util/json"
	// "github.com/hanzoai/cloud/clients/commerce/log"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginclient"
	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var _ = Describe("transaction", func() {
	Context("Permissions", func() {
		It("Should fail without token", func() {
			at := accessToken
			accessToken = "123"

			req := &transaction.Transaction{
				DestinationId:   "1",
				DestinationKind: "test-permission",
				Amount:          currency.Cents(100),
				Currency:        currency.USD,
				Type:            transaction.Deposit,
			}
			res := &ApiError{}
			cl.Post("/transaction", req, res)

			Expect(res.Error.Type).To(Equal("api-error"))
			Expect(res.Error.Message).To(Equal("Unable to retrieve organization associated with access token: Token contains an invalid number of segments"))

			accessToken = at
		})

		It("Should fail with token", func() {
			at := accessToken
			accessToken = pAccessToken

			req := &transaction.Transaction{
				DestinationId:   "2",
				DestinationKind: "test-permission",
				Amount:          currency.Cents(100),
				Currency:        currency.USD,
				Type:            transaction.Deposit,
			}
			res := &ApiError{}
			cl.Post("/transaction", req, res)

			Expect(res.Error.Type).To(Equal("api-error"))
			Expect(res.Error.Message).To(Equal("Token doesn't support this scope"))

			accessToken = at
		})
	})

	Context("Create", func() {
		It("Should work for Deposit", func() {
			req := &transaction.Transaction{
				DestinationId:   "1",
				DestinationKind: "test-deposit",
				Amount:          currency.Cents(100),
				Currency:        currency.USD,
				Type:            transaction.Deposit,
			}
			res := &transaction.Transaction{}
			cl.Post("/transaction", req, res)
			Expect(res.SourceId).To(Equal(req.SourceId))
			Expect(res.SourceKind).To(Equal(req.SourceKind))
			Expect(res.DestinationId).To(Equal(req.DestinationId))
			Expect(res.DestinationKind).To(Equal(req.DestinationKind))
			Expect(res.Type).To(Equal(req.Type))
			Expect(res.Currency).To(Equal(req.Currency))
			Expect(res.Amount).To(Equal(req.Amount))
			Expect(res.Type).To(Equal(req.Type))
		})

		It("Should drop Source for Deposit", func() {
			req := &transaction.Transaction{
				DestinationId:   "2",
				DestinationKind: "test-deposit",
				Amount:          currency.Cents(100),
				Currency:        currency.USD,
				Type:            transaction.Deposit,
				SourceId:        "2a",
				SourceKind:      "test-deposit",
			}
			res := &transaction.Transaction{}
			cl.Post("/transaction", req, res)
			Expect(res.SourceId).To(Equal(""))
			Expect(res.SourceKind).To(Equal(""))
			Expect(res.DestinationId).To(Equal(req.DestinationId))
			Expect(res.DestinationKind).To(Equal(req.DestinationKind))
			Expect(res.Type).To(Equal(req.Type))
			Expect(res.Currency).To(Equal(req.Currency))
			Expect(res.Amount).To(Equal(req.Amount))
			Expect(res.Type).To(Equal(req.Type))
		})

		It("Should error on Missing Destination for Deposit", func() {
			req := &transaction.Transaction{
				Amount:     currency.Cents(100),
				Currency:   currency.USD,
				Type:       transaction.Deposit,
				SourceId:   "3",
				SourceKind: "test-deposit",
			}
			res := &ApiError{}
			cl.Post("/transaction", req, res)

			Expect(res.Error.Type).To(Equal("api-error"))
			Expect(res.Error.Message).To(Equal("Destination is required"))
		})

		It("Should work for Withdraw", func() {
			req := &transaction.Transaction{
				DestinationId:   "1",
				DestinationKind: "test-withdraw",
				Amount:          currency.Cents(100),
				Currency:        currency.USD,
				Type:            transaction.Deposit,
			}
			res := &transaction.Transaction{}
			cl.Post("/transaction", req, res)

			req = &transaction.Transaction{
				SourceId:   "1",
				SourceKind: "test-withdraw",
				Amount:     currency.Cents(100),
				Currency:   currency.USD,
				Type:       transaction.Withdraw,
			}
			res = &transaction.Transaction{}
			cl.Post("/transaction", req, res)
			Expect(res.SourceId).To(Equal(req.SourceId))
			Expect(res.SourceKind).To(Equal(req.SourceKind))
			Expect(res.DestinationId).To(Equal(req.DestinationId))
			Expect(res.DestinationKind).To(Equal(req.DestinationKind))
			Expect(res.Type).To(Equal(req.Type))
			Expect(res.Currency).To(Equal(req.Currency))
			Expect(res.Amount).To(Equal(req.Amount))
			Expect(res.Type).To(Equal(req.Type))
		})

		It("Should error on Missing Source for Withdraw", func() {
			req := &transaction.Transaction{
				DestinationId:   "2",
				DestinationKind: "test-withdraw",
				Amount:          currency.Cents(100),
				Currency:        currency.USD,
				Type:            transaction.Deposit,
			}
			res := &transaction.Transaction{}
			cl.Post("/transaction", req, res)

			req = &transaction.Transaction{
				DestinationId:   "2",
				DestinationKind: "test-withdraw",
				Amount:          currency.Cents(100),
				Currency:        currency.USD,
				Type:            transaction.Withdraw,
			}
			res2 := &ApiError{}
			cl.Post("/transaction", req, res2)
			Expect(res2.Error.Type).To(Equal("api-error"))
			Expect(res2.Error.Message).To(Equal("Source is required"))
		})

		It("Should error on Insufficient funds for Withdraw", func() {
			req := &transaction.Transaction{
				DestinationId:   "3",
				DestinationKind: "test-withdraw",
				Amount:          currency.Cents(100),
				Currency:        currency.USD,
				Type:            transaction.Deposit,
			}
			res := &transaction.Transaction{}
			cl.Post("/transaction", req, res)

			req = &transaction.Transaction{
				SourceId:   "3",
				SourceKind: "test-withdraw",
				Amount:     currency.Cents(200),
				Currency:   currency.USD,
				Type:       transaction.Withdraw,
			}
			res2 := &ApiError{}
			cl.Post("/transaction", req, res2)
			Expect(res2.Error.Type).To(Equal("api-error"))
			Expect(res2.Error.Message).To(Equal("Source has insufficient funds"))
		})

		It("Should work for Transfer", func() {
			req := &transaction.Transaction{
				DestinationId:   "1",
				DestinationKind: "test-transfer",
				Amount:          currency.Cents(100),
				Currency:        currency.USD,
				Type:            transaction.Deposit,
			}
			res := &transaction.Transaction{}
			cl.Post("/transaction", req, res)

			req = &transaction.Transaction{
				SourceId:        "1",
				SourceKind:      "test-transfer",
				Amount:          currency.Cents(100),
				Currency:        currency.USD,
				Type:            transaction.Transfer,
				DestinationId:   "1a",
				DestinationKind: "test-transfer",
			}

			res = &transaction.Transaction{}
			cl.Post("/transaction", req, res)
			Expect(res.SourceId).To(Equal(req.SourceId))
			Expect(res.SourceKind).To(Equal(req.SourceKind))
			Expect(res.DestinationId).To(Equal(req.DestinationId))
			Expect(res.DestinationKind).To(Equal(req.DestinationKind))
			Expect(res.Type).To(Equal(req.Type))
			Expect(res.Currency).To(Equal(req.Currency))
			Expect(res.Amount).To(Equal(req.Amount))
			Expect(res.Type).To(Equal(req.Type))
		})

		It("Should error on Missing Source for Transfer", func() {
			req := &transaction.Transaction{
				DestinationId:   "2",
				DestinationKind: "test-transfer",
				Amount:          currency.Cents(100),
				Currency:        currency.USD,
				Type:            transaction.Deposit,
			}
			res := &transaction.Transaction{}
			cl.Post("/transaction", req, res)

			req = &transaction.Transaction{
				Amount:          currency.Cents(100),
				Currency:        currency.USD,
				Type:            transaction.Transfer,
				DestinationId:   "2a",
				DestinationKind: "test-transfer",
			}
			res2 := &ApiError{}
			cl.Post("/transaction", req, res2)
			Expect(res2.Error.Type).To(Equal("api-error"))
			Expect(res2.Error.Message).To(Equal("Source is required"))
		})

		It("Should error on Missing Source for Transfer", func() {
			req := &transaction.Transaction{
				DestinationId:   "3",
				DestinationKind: "test-transfer",
				Amount:          currency.Cents(100),
				Currency:        currency.USD,
				Type:            transaction.Deposit,
			}
			res := &transaction.Transaction{}
			cl.Post("/transaction", req, res)

			req = &transaction.Transaction{
				SourceId:   "3",
				SourceKind: "test-transfer",
				Amount:     currency.Cents(100),
				Currency:   currency.USD,
				Type:       transaction.Transfer,
			}
			res2 := &ApiError{}
			cl.Post("/transaction", req, res2)
			Expect(res2.Error.Type).To(Equal("api-error"))
			Expect(res2.Error.Message).To(Equal("Destination is required"))
		})

		It("Should error on Insufficient Funds for Transfer", func() {
			req := &transaction.Transaction{
				DestinationId:   "4",
				DestinationKind: "test-transfer",
				Amount:          currency.Cents(100),
				Currency:        currency.USD,
				Type:            transaction.Deposit,
			}
			res := &transaction.Transaction{}
			cl.Post("/transaction", req, res)

			req = &transaction.Transaction{
				SourceId:        "4",
				SourceKind:      "test-transfer",
				Amount:          currency.Cents(200),
				Currency:        currency.USD,
				Type:            transaction.Transfer,
				DestinationId:   "4a",
				DestinationKind: "test-transfer",
			}
			res2 := &ApiError{}
			cl.Post("/transaction", req, res2)
			Expect(res2.Error.Type).To(Equal("api-error"))
			Expect(res2.Error.Message).To(Equal("Source has insufficient funds"))
		})

		It("Should error for Hold", func() {
			req := &transaction.Transaction{
				DestinationId:   "error",
				DestinationKind: "error",
				Amount:          currency.Cents(100),
				Currency:        currency.USD,
				Type:            transaction.Hold,
			}
			res := &ApiError{}
			cl.Post("/transaction", req, res)
			Expect(res.Error.Type).To(Equal("api-error"))
			Expect(res.Error.Message).To(Equal("Use transaction/hold api to create holds"))
		})

		It("Should error for Circular transaction", func() {
			req := &transaction.Transaction{
				DestinationId:   "error",
				DestinationKind: "error",
				Amount:          currency.Cents(100),
				Currency:        currency.USD,
				Type:            transaction.Transfer,
				SourceId:        "error",
				SourceKind:      "error",
			}
			res := &ApiError{}
			cl.Post("/transaction", req, res)
			Expect(res.Error.Type).To(Equal("api-error"))
			Expect(res.Error.Message).To(Equal("Source and Destination cannot be the same"))
		})

		It("Should error for Unknown Type", func() {
			req := &transaction.Transaction{
				DestinationId:   "error",
				DestinationKind: "error",
				Amount:          currency.Cents(100),
				Currency:        currency.USD,
				Type:            "wutwut",
			}
			res := &ApiError{}
			cl.Post("/transaction", req, res)
			Expect(res.Error.Type).To(Equal("api-error"))
			Expect(res.Error.Message).To(Equal("Type is invalid"))
		})

		It("Should error for Missing Amount", func() {
			req := &transaction.Transaction{
				DestinationId:   "error",
				DestinationKind: "error",
				Currency:        currency.USD,
				Type:            transaction.Deposit,
			}
			res := &ApiError{}
			cl.Post("/transaction", req, res)
			Expect(res.Error.Type).To(Equal("api-error"))
			Expect(res.Error.Message).To(Equal("Amount cannot be 0"))
		})

		It("Should error for Missing Currency", func() {
			req := &transaction.Transaction{
				DestinationId:   "error",
				DestinationKind: "error",
				Amount:          currency.Cents(100),
				Type:            transaction.Deposit,
			}
			res := &ApiError{}
			cl.Post("/transaction", req, res)
			Expect(res.Error.Type).To(Equal("api-error"))
			Expect(res.Error.Message).To(Equal("Currency is required"))
		})
	})

	Context("List", func() {
		It("Should work", func() {
			req := &transaction.Transaction{
				DestinationId:   "1",
				DestinationKind: "test-list",
				Amount:          currency.Cents(100),
				Currency:        currency.USD,
				Type:            transaction.Deposit,
			}
			res := &transaction.Transaction{}
			cl.Post("/transaction", req, res)

			req = &transaction.Transaction{
				DestinationId:   "1",
				DestinationKind: "test-list",
				Amount:          currency.Cents(100),
				Currency:        currency.BTC,
				Type:            transaction.Deposit,
			}
			res = &transaction.Transaction{}
			cl.Post("/transaction", req, res)

			req = &transaction.Transaction{
				SourceId:   "1",
				SourceKind: "test-list",
				Amount:     currency.Cents(100),
				Currency:   currency.USD,
			}
			res = &transaction.Transaction{}
			cl.Post("/transaction/hold", req, res)

			req = &transaction.Transaction{
				DestinationId:   "1",
				DestinationKind: "test-list",
				Amount:          currency.Cents(50),
				Currency:        currency.USD,
				Type:            transaction.Deposit,
			}
			res = &transaction.Transaction{}
			cl.Post("/transaction", req, res)

			req = &transaction.Transaction{
				SourceId:   "1",
				SourceKind: "test-list",
				Amount:     currency.Cents(25),
				Currency:   currency.USD,
				Type:       transaction.Withdraw,
			}
			res = &transaction.Transaction{}
			cl.Post("/transaction", req, res)

			req = &transaction.Transaction{
				SourceId:        "1",
				SourceKind:      "test-list",
				Amount:          currency.Cents(1),
				Currency:        currency.USD,
				Type:            transaction.Transfer,
				DestinationId:   "2",
				DestinationKind: "test-list",
			}
			res = &transaction.Transaction{}
			cl.Post("/transaction", req, res)

			res2 := &util.TransactionDatas{}
			cl.Get("/transaction/test-list/1", res2)

			Expect(res2.Kind).To(Equal("test-list"))
			Expect(res2.Id).To(Equal("1"))
			Expect(res2.Data[currency.USD] != nil).To(Equal(true))
			Expect(res2.Data[currency.BTC] != nil).To(Equal(true))

			Expect(len(res2.Data[currency.USD].Transactions)).To(Equal(5))
			Expect(res2.Data[currency.USD].Balance).To(Equal(currency.Cents(124)))
			Expect(res2.Data[currency.BTC].Balance).To(Equal(currency.Cents(100)))
			Expect(res2.Data[currency.USD].Holds).To(Equal(currency.Cents(100)))
		})
	})

	Context("Hold", func() {
		It("Should work", func() {
			req := &transaction.Transaction{
				DestinationId:   "1",
				DestinationKind: "test-hold",
				Amount:          currency.Cents(100),
				Currency:        currency.USD,
				Type:            transaction.Deposit,
			}
			res := &transaction.Transaction{}
			cl.Post("/transaction", req, res)

			req = &transaction.Transaction{
				SourceId:   "1",
				SourceKind: "test-hold",
				Amount:     currency.Cents(100),
				Currency:   currency.USD,
			}
			res = &transaction.Transaction{}
			cl.Post("/transaction/hold", req, res)

			res2 := &util.TransactionDatas{}
			cl.Get("/transaction/test-hold/1", res2)

			Expect(res2.Kind).To(Equal("test-hold"))
			Expect(res2.Id).To(Equal("1"))
			Expect(res2.Data[currency.USD] != nil).To(Equal(true))
			Expect(res2.Data[currency.USD].Holds).To(Equal(currency.Cents(100)))
		})

		It("Should error on Insufficient Funds", func() {
			req := &transaction.Transaction{
				SourceId:   "2",
				SourceKind: "test-hold",
				Amount:     currency.Cents(100),
				Currency:   currency.BTC,
			}
			res := &ApiError{}
			cl.Post("/transaction/hold", req, res)

			Expect(res.Error.Type).To(Equal("api-error"))
			Expect(res.Error.Message).To(Equal("Source has insufficient funds"))
		})

		It("Should error on Missing Amount", func() {
			req := &transaction.Transaction{
				SourceId:   "error",
				SourceKind: "error",
				Currency:   currency.BTC,
			}
			res := &ApiError{}
			cl.Post("/transaction/hold", req, res)

			Expect(res.Error.Type).To(Equal("api-error"))
			Expect(res.Error.Message).To(Equal("Amount cannot be 0"))
		})

		It("Should error on Missing Currency", func() {
			req := &transaction.Transaction{
				SourceId:   "error",
				SourceKind: "error",
				Amount:     currency.Cents(100),
			}
			res := &ApiError{}
			cl.Post("/transaction/hold", req, res)

			Expect(res.Error.Type).To(Equal("api-error"))
			Expect(res.Error.Message).To(Equal("Currency is required"))
		})
	})

	type RemoveHoldReq struct {
		Id string `json:"id"`
	}

	Context("Hold/Remove", func() {
		It("Should work", func() {
			req := &transaction.Transaction{
				DestinationId:   "1",
				DestinationKind: "test-hold-remove",
				Amount:          currency.Cents(100),
				Currency:        currency.USD,
				Type:            transaction.Deposit,
			}
			res := &transaction.Transaction{}
			cl.Post("/transaction", req, res)

			req = &transaction.Transaction{
				SourceId:   "1",
				SourceKind: "test-hold-remove",
				Amount:     currency.Cents(100),
				Currency:   currency.USD,
			}
			res = &transaction.Transaction{}
			cl.Post("/transaction/hold", req, res)

			cl.Delete("/transaction/hold/" + res.Id_)

			res3 := &util.TransactionDatas{}
			cl.Get("/transaction/test-hold-remove/1", res3)

			Expect(res3.Kind).To(Equal("test-hold-remove"))
			Expect(res3.Id).To(Equal("1"))
			Expect(res3.Data[currency.USD] != nil).To(Equal(true))
			Expect(res3.Data[currency.USD].Holds).To(Equal(currency.Cents(0)))
		})
	})
})
