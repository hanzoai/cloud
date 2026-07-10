package test

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("models/user", t)
}

var (
	ctx ae.Context
	db  *datastore.Datastore
)

// Setup test context
var _ = BeforeSuite(func() {
	ctx = ae.NewContext()
	db = datastore.New(ctx)
})

// Tear-down test context
var _ = AfterSuite(func() {
	ctx.Close()
})

var _ = Describe("User", func() {
	Context("GetById", func() {
		var usr *user.User

		Before(func() {
			usr = user.Fake(db)
			usr.MustCreate()
		})

		It("should retrieve entity from datastore by email", func() {
			usr2 := user.New(db)
			usr2.MustGetById(usr.Email)
			Expect(usr2.Email).To(Equal(usr2.Email))
			Expect(usr2.Name()).To(Equal(usr2.Name()))
		})

		It("should retrieve entity from datastore by username", func() {
			usr2 := user.New(db)
			usr2.MustGetById(usr.Username)
			Expect(usr2.Email).To(Equal(usr2.Email))
			Expect(usr2.Name()).To(Equal(usr2.Name()))
		})
	})
})
