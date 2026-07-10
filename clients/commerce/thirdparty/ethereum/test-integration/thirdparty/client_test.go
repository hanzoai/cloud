package test

import (
	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"

	"github.com/hanzoai/cloud/clients/commerce/util/json"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"math/big"
)

var _ = Describe("client.GasPrice2", func() {
	It("should work", func() {
		gasPrice, res, err := client.GasPrice2()
		log.Info("RESPONSE %v", json.Encode(res))
		Expect(gasPrice).ToNot(Equal(0))
		Expect(gasPrice.Cmp(big.NewInt(int64(res.SafeLow + 1)))).To(Equal(0))
		// Expect(res.FastestWait <= res.FastWait).To(BeTrue())
		// Expect(res.FastWait <= res.AverageWait).To(BeTrue())
		// Expect(res.AverageWait <= res.SafeLowWait).To(BeTrue())
		// Expect(res.Fastest >= res.Fast).To(BeTrue())
		// Expect(res.Fast >= res.Average).To(BeTrue())
		// Expect(res.Average >= res.SafeLow).To(BeTrue())
		Expect(err).ToNot(HaveOccurred())
	})
})
