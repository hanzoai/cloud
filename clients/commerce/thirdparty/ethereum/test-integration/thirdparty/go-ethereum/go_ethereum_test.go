package test

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/thirdparty/ethereum"
	"github.com/hanzoai/cloud/clients/commerce/log"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("thirdparty/ethereum/go-ethereum", t)
}

// PubkeyToAddress is covered in this test too
var _ = Describe("ether.GenerateKeyPair", func() {
	It("should work", func() {
		priv, pub, add, err := ethereum.GenerateKeyPair()
		Expect(err).ToNot(HaveOccurred())

		log.Debug("\nPrivate: %v\nPublic: %v\nAddress: %v\n", priv, pub, add)

		Expect(len(priv)).To(Equal(64))
		Expect(len(pub)).To(Equal(128))
		Expect(len(add)).To(Equal(42))
	})

	It("should be random", func() {
		priv, pub, add, err := ethereum.GenerateKeyPair()
		Expect(err).ToNot(HaveOccurred())

		priv2, pub2, add2, err := ethereum.GenerateKeyPair()
		Expect(err).ToNot(HaveOccurred())

		log.Debug("\nAdd1: %s\nAdd2: %s", add, add2)

		Expect(priv).ToNot(Equal(priv2))
		Expect(pub).ToNot(Equal(pub2))
		Expect(add).ToNot(Equal(add2))
	})
})
