package integration

import (
	"fmt"

	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/test-integration/api/checkout/processors/requests"
	"github.com/hanzoai/cloud/clients/commerce/util/json"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var _ = Describe("/checkout/authorize", func() {
	path := "/authorize"

	Context("Authorize new user", func() {
		It("Should work with Ethereum Order", func() {
			w := cl.Post(path, requests.ValidEthereumOrder, nil)

			Expect(w.Code).To(Equal(200))

			log.Debug("JSON %v", w.Body)

			// Order info should be in the dv
			ord := order.New(db)

			err := json.DecodeBuffer(w.Body, &ord)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(ord.PaymentIds)).To(Equal(0))

			log.Debug("Order %v", ord)

			// Order should be in db
			key, _, err := order.Query(db).IdExists(ord.Id())
			log.Debug("Err %v", err)

			Expect(err).ToNot(HaveOccurred())
			Expect(key).ToNot(BeNil())

			usr := user.New(db)
			err = usr.GetById(ord.UserId)

			Expect(err).ToNot(HaveOccurred())
			Expect(usr.Key()).ToNot(BeNil())
		})

		It("Should not work with Not-Ethereum", func() {
			w := cl.Post(path, requests.InvalidCurrencyEthereumOrder, nil)
			Expect(w.Code).To(Equal(400))
		})

		// Should create:
		// Order
		//   Order Wallet w/1 Account
		// User
		//   User Wallet w/1 Account
		It("Should work with Ethereum TokenSale", func() {
			w := cl.Post(path, fmt.Sprintf(requests.ValidEthereumTokenSaleOrder, ts.Id()), nil)

			Expect(w.Code).To(Equal(200))

			log.Debug("JSON %v", w.Body)

			// Order info should be in the dv
			ord := order.New(db)

			err := json.DecodeBuffer(w.Body, &ord)
			Expect(err).ToNot(HaveOccurred())
			Expect(ord.TokenSaleId).To(Equal(ts.Id()))
			Expect(ord.WalletId).ToNot(Equal(""))

			wal := ord.Wallet
			Expect(wal.Id()).To(Equal(ord.WalletId))

			// Wallet passphrase shouldn't be captured in return
			Expect(ord.WalletPassphrase).To(Equal(""))

			log.Debug("Order %v", ord)

			// Order should be in db
			ord2 := order.New(db)
			err = ord2.GetById(ord.Id())
			Expect(err).ToNot(HaveOccurred())

			// Wallet passphrase should be in db
			Expect(ord2.WalletPassphrase).ToNot(Equal(""))
			Expect(len(wal.Accounts)).To(Equal(1))

			// Try to decrypt account
			a := wal.Accounts[0]
			Expect(a.PrivateKey).To(Equal(""))
			err = a.Decrypt([]byte(ord2.WalletPassphrase))
			Expect(err).ToNot(HaveOccurred())
			Expect(a.PrivateKey).ToNot(Equal(""))

			// User should be in db
			usr := user.New(db)
			err = usr.GetById(ord.UserId)

			Expect(err).ToNot(HaveOccurred())
			Expect(usr.Key()).ToNot(BeNil())
			Expect(usr.WalletId).ToNot(Equal(""))

			// User wallet should exist
			err = usr.LoadWallet(usr.Datastore())
			Expect(err).ToNot(HaveOccurred())

			uWal := usr.Wallet
			Expect(len(uWal.Accounts)).To(Equal(1))

			// Try to decrypt account
			uA := uWal.Accounts[0]
			Expect(uA.PrivateKey).To(Equal(""))
			err = uA.Decrypt([]byte("123456"))
			Expect(err).ToNot(HaveOccurred())
			Expect(uA.PrivateKey).ToNot(Equal(""))
		})

		It("Should not work with Missing TokenSaleId", func() {
			w := cl.Post(path, requests.InvalidEthereumNoTokenSaleIdTokenSaleOrder, nil)
			Expect(w.Code).To(Equal(400))
		})

		It("Should not work with Missing TokenSale Passphrase", func() {
			w := cl.Post(path, fmt.Sprintf(requests.InvalidEthereumPassphraseTokenSaleOrder, ts.Id()), nil)
			Expect(w.Code).To(Equal(400))
		})
	})
})
