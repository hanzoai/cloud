package test

// import (
// 	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
// 	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
// 	"github.com/hanzoai/cloud/clients/commerce/util/fake"
// 	"github.com/hanzoai/cloud/clients/commerce/log"
// 	"math/rand"

// 	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
// )

// var _ = Describe("transaction", func() {
// 	Context("New transaction", func() {
// 		req := new(transaction.Transaction)
// 		res := new(transaction.Transaction)

// 		Before(func() {
// 			req = transaction.Fake(db)
// 			res = transaction.New(db)

// 			// Create new transaction
// 			cl.Post("/transaction", req, res)
// 		})

// 		It("Should create new transactions", func() {
// 			Expect(res.DestinationId).To(Equal(req.DestinationId))
// 			Expect(res.DestinationKind).To(Equal(req.DestinationKind))
// 			Expect(res.SourceId).To(Equal(req.SourceId))
// 			Expect(res.Type).To(Equal(req.Type))
// 			Expect(res.Currency).To(Equal(req.Currency))
// 			Expect(res.Amount).To(Equal(req.Amount))
// 		})
// 	})

// 	Context("Get transaction", func() {
// 		req := new(transaction.Transaction)
// 		res := new(transaction.Transaction)

// 		Before(func() {
// 			// Create transaction
// 			req = transaction.Fake(db)
// 			req.MustCreate()

// 			// Make response for verification
// 			res = transaction.New(db)

// 			// Get transaction
// 			w := cl.Get("/transaction/"+req.Id(), res)
// 			log.Warn(w.Body.String())

// 		})

// 		It("Should get transactions", func() {
// 			Expect(res.DestinationId).To(Equal(req.DestinationId))
// 			Expect(res.DestinationKind).To(Equal(req.DestinationKind))
// 			Expect(res.SourceId).To(Equal(req.SourceId))
// 			Expect(res.Type).To(Equal(req.Type))
// 			Expect(res.Currency).To(Equal(req.Currency))
// 			Expect(res.Amount).To(Equal(req.Amount))
// 		})
// 	})

// 	Context("Put transaction", func() {
// 		trn := new(transaction.Transaction)
// 		res := new(transaction.Transaction)
// 		req := new(transaction.Transaction)

// 		Before(func() {
// 			trn = transaction.Fake(db)
// 			trn.MustCreate()

// 			// Create transaction request
// 			req = transaction.Fake(db)

// 			// Update transaction
// 			cl.Put("/transaction/"+trn.Id(), req, res)
// 		})

// 		It("Should put transaction", func() {
// 			Expect(res.Id_).To(Equal(trn.Id()))
// 			Expect(res.DestinationId).To(Equal(req.DestinationId))
// 			Expect(res.DestinationKind).To(Equal(req.DestinationKind))
// 			Expect(res.SourceId).To(Equal(req.SourceId))
// 			Expect(res.Type).To(Equal(req.Type))
// 			Expect(res.Currency).To(Equal(req.Currency))
// 			Expect(res.Amount).To(Equal(req.Amount))
// 		})
// 	})

// 	Context("patch transaction", func() {
// 		trn := new(transaction.Transaction)
// 		res := new(transaction.Transaction)

// 		req := struct {
// 			DestinationId   string           `json:"destinationId"`
// 			DestinationKind string           `json:"destinationKind"`
// 			Type            transaction.Type `json:"type"`
// 			Currency        currency.Type    `json:"currency"`
// 			Amount          currency.Cents   `json:"amount"`
// 			Test            bool             `json:"test"`
// 			SourceId        string           `json:"sourceId"`
// 		}{
// 			fake.Id(),
// 			"User",
// 			"deposit",
// 			currency.Fake(),
// 			currency.Cents(rand.Intn(10000)),
// 			true,
// 			fake.Id(),
// 		}

// 		Before(func() {
// 			trn = transaction.Fake(db)
// 			trn.MustCreate()

// 			// Update transaction
// 			cl.Patch("/transaction/"+trn.Id(), req, res)
// 			log.JSON(req)
// 			log.JSON(res)
// 		})

// 		It("Should patch transaction", func() {
// 			Expect(res.Id_).To(Equal(trn.Id()))
// 			Expect(res.DestinationId).To(Equal(req.DestinationId))
// 			Expect(res.DestinationKind).To(Equal(req.DestinationKind))
// 			Expect(res.SourceId).To(Equal(req.SourceId))
// 			Expect(res.Type).To(Equal(req.Type))
// 			Expect(res.Currency).To(Equal(req.Currency))
// 			Expect(res.Amount).To(Equal(req.Amount))
// 		})
// 	})

// 	Context("Delete transaction", func() {
// 		var trn *transaction.Transaction
// 		var id string

// 		Before(func() {
// 			// Create transaction
// 			trn = transaction.Fake(db)
// 			trn.MustCreate()

// 			// Delete it
// 			cl.Delete("/transaction/" + trn.Id())

// 			id = trn.Id()
// 		})

// 		It("Should delete transactions", func() {
// 			trn2 := transaction.New(db)
// 			err := trn2.GetById(id)
// 			Expect(err).ToNot(BeNil())
// 		})
// 	})
// })
