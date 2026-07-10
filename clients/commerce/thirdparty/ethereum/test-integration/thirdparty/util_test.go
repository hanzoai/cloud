package test

import (
	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"

	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	blockchainutil "github.com/hanzoai/cloud/clients/commerce/util/blockchain"
)

var _ = Describe("client.GasPrice2", func() {
	It("should work", func() {
		ord := order.New(db)
		wal, err := ord.GetOrCreateWallet(db)
		Expect(err).NotTo(HaveOccurred())

		ord.MustCreate()

		usr := user.New(db)
		usr.MustCreate()

		u, o, w, err := blockchainutil.GetUserOrderByWallet(db, wal.Id())

		Expect(err).NotTo(HaveOccurred())

		Expect(u.Id()).To(Equal(usr.Id()))
		Expect(o.Id()).To(Equal(ord.Id()))
		Expect(w.Id()).To(Equal(wal.Id()))
	})
})
