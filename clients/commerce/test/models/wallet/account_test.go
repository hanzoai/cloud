package test

import (
	"strings"

	"github.com/hanzoai/cloud/clients/commerce/models/wallet"
	"github.com/hanzoai/cloud/clients/commerce/util/json"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var _ = Describe("Account", func() {
	var acc *wallet.Account
	var password string

	BeforeEach(func() {
		_, acc, password = wallet.Fake(db)
	})

	Context("Encrypt/Decrypt", func() {
		It("should Encrypt/Decrypt properly", func() {
			enc := acc.Encrypted
			priv := acc.PrivateKey

			acc.PrivateKey = ""

			// Get the private key from the encrypted data
			err := acc.Decrypt([]byte(password))
			Expect(err).ToNot(HaveOccurred())
			Expect(acc.PrivateKey).To(Equal(priv))

			acc.Encrypted = ""

			err = acc.Encrypt([]byte(password))
			Expect(err).ToNot(HaveOccurred())

			// Should have random IV block each encryption system
			Expect(acc.Encrypted).ToNot(Equal(enc))

			acc.PrivateKey = ""

			err = acc.Decrypt([]byte(password))
			Expect(err).ToNot(HaveOccurred())
			Expect(acc.PrivateKey).To(Equal(priv))
		})

		It("should error with NoPrivateKeySetError", func() {
			acc.PrivateKey = ""

			err := acc.Encrypt([]byte(password))
			Expect(err).To(Equal(wallet.ErrorNoPrivateKeySet))
		})

		It("should error with NoEncryptedKeyFound", func() {
			acc.Encrypted = ""

			err := acc.Decrypt([]byte(password))
			Expect(err).To(Equal(wallet.ErrorNoEncryptedKeyFound))
		})

		It("should never serialize the privatekey", func() {
			Expect(acc.PrivateKey).ToNot(Equal(""))
			Expect(acc.Encrypted).ToNot(Equal(""))

			a2 := wallet.Account{}
			jsn := json.Encode(acc)

			Expect(strings.Index(jsn, acc.PrivateKey)).To(Equal(-1))
			Expect(strings.Index(jsn, acc.Encrypted)).ToNot(Equal(-1))

			err := json.DecodeBytes([]byte(jsn), &a2)
			Expect(err).ToNot(HaveOccurred())

			Expect(a2.PrivateKey).To(Equal(""))
			Expect(a2.Encrypted).To(Equal(acc.Encrypted))
		})
	})

	Context("Delete", func() {
		It("should work properly", func() {
			Expect(acc.Deleted).To(Equal(false))
			acc.Delete()
			Expect(acc.Deleted).To(Equal(true))
		})
	})
})
