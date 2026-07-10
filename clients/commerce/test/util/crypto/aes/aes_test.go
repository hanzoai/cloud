package test

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/util/crypto/aes"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/util/rand"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var msg = "This is the test message. It better pass."

func Test(t *testing.T) {
	Setup("util/crypto/aes", t)
}

var _ = Describe("aes.RemoveBase64Padding", func() {
	It("should work", func() {
		str := aes.RemoveBase64Padding("ABC=============")
		Expect(str).To(Equal("ABC"))
	})
})

var _ = Describe("aes.Pad", func() {
	It("should work", func() {
		bytes := aes.Pad([]byte{65, 66, 67})
		Expect(bytes).To(Equal([]byte{65, 66, 67, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13, 13}))
	})
})

var _ = Describe("aes.AddBase64Padding", func() {
	It("should work", func() {
	})
})

var _ = Describe("aes.Unpad", func() {
	It("should work", func() {
	})
})

var _ = Describe("aes.AES128KeyFromPassword", func() {
	It("should generate a rederivable key", func() {
		salt := rand.SecretKey()
		password := rand.ShortPassword()

		key, err := aes.AES128KeyFromPassword([]byte(password), []byte(salt))
		Expect(err).ToNot(HaveOccurred())
		Expect(key).ToNot(Equal(password))
		Expect(len(key)).To(Equal(16))

		key2, err := aes.AES128KeyFromPassword([]byte(password), []byte(salt))
		Expect(err).ToNot(HaveOccurred())
		Expect(err).ToNot(HaveOccurred())
		Expect(key2).ToNot(Equal(password))
		Expect(len(key2)).To(Equal(16))

		Expect(key).To(Equal(key2))
	})
})

var _ = Describe("aes.EncryptCBC / aes.DecryptCBC 128bit", func() {
	key := []byte("CBCEncryptionKey")

	It("should work with keys", func() {
		str, err := aes.EncryptCBC(key, msg)
		Expect(err).ToNot(HaveOccurred())
		Expect(str).ToNot(Equal(""))

		decodedMsg, err := aes.DecryptCBC(key, str)
		Expect(err).ToNot(HaveOccurred())
		Expect(decodedMsg).To(Equal(msg))
	})
})

var _ = Describe("aes.EncryptCBC / aes.DecryptCBC 256bit", func() {
	key := []byte("CBCEncryptionKey_256bits")

	It("should work with keys", func() {
		str, err := aes.EncryptCBC(key, msg)
		Expect(err).ToNot(HaveOccurred())
		Expect(str).ToNot(Equal(""))

		decodedMsg, err := aes.DecryptCBC(key, str)
		Expect(err).ToNot(HaveOccurred())
		Expect(decodedMsg).To(Equal(msg))
	})
})

var _ = Describe("aes.EncryptCBC / aes.DecryptCBC 512bit", func() {
	key := []byte("CBCEncryptionKey_512bits_hugekey")

	It("should work with keys", func() {
		str, err := aes.EncryptCBC(key, msg)
		Expect(err).ToNot(HaveOccurred())
		Expect(str).ToNot(Equal(""))

		decodedMsg, err := aes.DecryptCBC(key, str)
		Expect(err).ToNot(HaveOccurred())
		Expect(decodedMsg).To(Equal(msg))
	})
})

var _ = Describe("aes.EncryptCBC / aes.DecryptCBC Other", func() {
	It("should break while encrypting unsupported key lengths", func() {
		key := []byte("EncryptionKey")
		str, err := aes.EncryptCBC(key, msg)
		Expect(err).To(HaveOccurred())
		Expect(str).To(Equal(""))
	})

	It("should break while decrypting with unsupported key lengths", func() {
		key := []byte("CBCEncryptionKey")
		str, err := aes.EncryptCBC(key, msg)
		Expect(err).ToNot(HaveOccurred())
		Expect(str).ToNot(Equal(""))

		decodedMsg, err := aes.DecryptCBC([]byte("EncryptionKey"), str)
		Expect(err).To(HaveOccurred())
		Expect(decodedMsg).To(Equal(""))
	})

	It("should break while decrypting with incorrect key", func() {
		key := []byte("CBCEncryptionKey")
		str, err := aes.EncryptCBC(key, msg)
		Expect(err).ToNot(HaveOccurred())
		Expect(str).ToNot(Equal(""))

		decodedMsg, err := aes.DecryptCBC([]byte("BadEncryptionKey"), str)
		if err != nil {
			log.Error("Incase this is somehow Decrypted Again...?\n%s =? %s\nDecoded %s", key, []byte("BadEncryptionKey"), decodedMsg)
			Expect(err).To(Equal(aes.UnpadError))
			Expect(decodedMsg).To(Equal(""))
		} else {
			Expect(decodedMsg).ToNot(Equal("This is the test message. It better pass."))
		}
	})
})
