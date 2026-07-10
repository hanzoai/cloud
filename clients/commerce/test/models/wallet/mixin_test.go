package test

import (
	"github.com/hanzoai/cloud/clients/commerce/models/wallet"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

type WalletHolderer struct {
	wallet.WalletHolder
}

var _ = Describe("WalletHolder", func() {
	Context("CreateWallet", func() {
		var wh WalletHolderer

		Before(func() {
			wh = WalletHolderer{}
		})

		It("should create a wallet properly", func() {
			Expect(wh.WalletId).To(Equal(""))
			w, err := wh.GetOrCreateWallet(db)

			Expect(err).ToNot(HaveOccurred())
			Expect(w.Id()).To(Equal(wh.WalletId))

			w2 := wallet.New(db)
			w2.GetById(w.Id())

			Expect(w2.Id()).ToNot(Equal(""))
			Expect(w2.Id()).To(Equal(w.Id()))
			Expect(w2.Id()).To(Equal(wh.WalletId))
		})

		It("should get a wallet properly", func() {
			w := wallet.New(db)
			w.MustCreate()

			wh.WalletId = w.Id()
			w2, err := wh.GetOrCreateWallet(db)

			Expect(err).ToNot(HaveOccurred())

			Expect(w2.Id()).ToNot(Equal(""))
			Expect(w2.Id()).To(Equal(w.Id()))
			Expect(w2.Id()).To(Equal(wh.WalletId))
		})
	})
})
