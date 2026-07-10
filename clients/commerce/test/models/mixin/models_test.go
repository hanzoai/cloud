package test

import . "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"

var _ = Describe("models/mixin Model", func() {
	Context("Model.SetKey", func() {
		It("should set key for model", func() {
			// Create a new user and store using Model mixin
			db.SetNamespace("suchtees")
			usr := newUser(db)
			usr.Name = "Justus"
			usr.MustCreate()

			usr2 := newUser(db)
			usr2.SetKey(usr.Id())
			Expect(usr2.Key()).To(Equal(usr.Key()))
			Expect(usr2.Key().Namespace()).To(Equal(usr.Key().Namespace()))
		})
	})

	Context("Model.Put", func() {
		It("should save entity to datastore", func() {
			// Create a new user and store using Model mixin
			usr := newUser(db)
			usr.Name = "Justin"
			usr.MustPut()

			// Manually retrieve to ensure it was saved properly
			usr2 := newUser(db)
			usr2.MustGet(usr.Key())
			Expect(usr2.Name).To(Equal(usr.Name))
		})
	})

	Context("Model.Get", func() {
		It("should retrieve entity from datastore", func() {
			// Manually create a new user and store in datastore
			usr := newUser(db)
			usr.Name = "Dustin"
			usr.MustCreate()

			// Retrieve usr from datastore using Model mixin
			usr2 := newUser(db)
			usr2.MustGet(usr.Key())
			Expect(usr2.Name).To(Equal(usr.Name))
		})
	})

	Context("Model.GetById", func() {
		It("should retrieve entity from datastore by Id()", func() {
			// Manually create a new user and store in datastore
			usr := newUser(db)
			usr.Email = "dev@hanzo.ai"
			usr.Name = "Dustin"
			usr.MustCreate()

			// Retrieve usr from datastore using Model mixin
			usr2 := newUser(db)
			usr2.MustGetById(usr.Id())
			Expect(usr2.Email).To(Equal(usr.Email))
			Expect(usr2.Name).To(Equal(usr.Name))
		})
	})
})
