package test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/hanzoai/cloud/clients/commerce/util/permission"
)

var _ = Describe("models/mixin AccessToken", func() {
	Context("AccessToken.AddToken", func() {
		It("Should be able to create a token", func() {
			// Create a new user and store using Model mixin
			user := newUser(db)
			user.Name = "AddToken"
			user.SecretKey = []byte("AAA")

			// Create the token for looking up
			tokenStr := user.AddToken("add-test", permission.Admin)
			err := user.Put()
			Expect(err).NotTo(HaveOccurred())
			Expect(len(user.Tokens)).To(Equal(1))

			user.AddToken("add-test-2", permission.Admin)
			user.AddToken("add-test-3", permission.Admin)
			Expect(len(user.Tokens)).To(Equal(3))

			// Manually retrieve to ensure it was saved properly
			user2 := newUser(db)
			_, err = user2.GetWithAccessToken(tokenStr)
			Expect(err).NotTo(HaveOccurred())
			Expect(user2.Name).To(Equal(user.Name))
		})
	})

	Context("AccessToken.RemoveToken", func() {
		It("Should be able to remove a token", func() {
			// Create a new user and store using Model mixin
			user := newUser(db)
			user.Name = "RemoveToken"
			user.SecretKey = []byte("BBB")

			// Create the token for looking up
			removedstr := user.AddToken("removed-token", permission.Admin)
			validstr := user.AddToken("valid-token", permission.Admin)
			Expect(len(user.Tokens)).To(Equal(2))

			// Should remove token
			user.RemoveToken("removed-token")
			Expect(len(user.Tokens)).To(Equal(1))

			// This should be a noop, if it removes a token something is fucked
			user.RemoveToken("removed-token")
			Expect(len(user.Tokens)).To(Equal(1))

			err := user.Put()
			Expect(err).NotTo(HaveOccurred())

			// Manually retrieve to ensure it was saved properly
			user2 := newUser(db)
			_, err = user2.GetWithAccessToken(removedstr)
			Expect(err).To(HaveOccurred())

			_, err = user2.GetWithAccessToken(validstr)
			Expect(err).NotTo(HaveOccurred())

			Expect(user2.Name).To(Equal(user.Name))
		})
	})

	Context("AccessToken.GetWithAccessToken", func() {
		It("Should be able to retrieve model by using token", func() {
			// Create a new user and store using Model mixin
			user := newUser(db)
			user.Name = "GetWithAccessToken"
			user.SecretKey = []byte("CCC")

			// Create the token for looking up
			tokstr := user.AddToken("get-with", permission.Admin)
			err := user.Put()
			Expect(err).NotTo(HaveOccurred())

			// Manually retrieve to ensure it was saved properly
			user2 := newUser(db)
			_, err = user2.GetWithAccessToken(tokstr)
			Expect(err).NotTo(HaveOccurred())
			Expect(user2.Name).To(Equal(user.Name))

		})
	})
})
