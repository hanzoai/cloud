package test

import (
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/models/variant"
	"github.com/hanzoai/cloud/clients/commerce/util/fake"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var _ = Describe("variant", func() {
	Context("New variant", func() {
		req := new(variant.Variant)
		res := new(variant.Variant)

		Before(func() {
			prod := product.Fake(db)
			prod.MustCreate()
			req = variant.Fake(db, prod.Id())
			res = variant.New(db)

			// Create new variant
			cl.Post("/variant", req, res)
		})

		It("Should create new variants", func() {
			Expect(res.ProductId).To(Equal(req.ProductId))
			Expect(res.SKU).To(Equal(req.SKU))
			Expect(res.Available).To(Equal(req.Available))
			Expect(res.Name).To(Equal(req.Name))
			Expect(res.Inventory).To(Equal(req.Inventory))
			Expect(res.Sold).To(Equal(req.Sold))
			Expect(res.Taxable).To(Equal(req.Taxable))
		})
	})

	Context("Get variant", func() {
		req := new(variant.Variant)
		res := new(variant.Variant)

		Before(func() {
			prod := product.Fake(db)
			prod.MustCreate()
			req = variant.Fake(db, prod.Id())
			req.MustCreate()

			res = variant.New(db)

			cl.Get("/variant/"+req.Id(), res)
		})

		It("Should get variants", func() {
			Expect(res.ProductId).To(Equal(req.ProductId))
			Expect(res.SKU).To(Equal(req.SKU))
			Expect(res.Available).To(Equal(req.Available))
			Expect(res.Name).To(Equal(req.Name))
			Expect(res.Inventory).To(Equal(req.Inventory))
			Expect(res.Sold).To(Equal(req.Sold))
			Expect(res.Taxable).To(Equal(req.Taxable))
		})
	})

	Context("Patch variant", func() {
		vari := new(variant.Variant)
		res := new(variant.Variant)

		req := struct {
			ProductId string `json:"productId"`
			SKU       string `json:"sku"`
		}{
			fake.Id(),
			fake.SKU(),
		}

		Before(func() {
			// Create product
			prod := product.Fake(db)
			prod.MustCreate()

			// Create variant
			vari = variant.Fake(db, prod.Id())
			vari.MustCreate()

			// Patch variant
			cl.Patch("/variant/"+vari.Id(), req, res)
		})

		It("Should patch variant", func() {
			Expect(res.Id_).To(Equal(vari.Id()))
			Expect(res.ProductId).To(Equal(req.ProductId))
			Expect(res.SKU).To(Equal(req.SKU))
			Expect(res.Available).To(Equal(vari.Available))
			Expect(res.Name).To(Equal(vari.Name))
			Expect(res.Inventory).To(Equal(vari.Inventory))
			Expect(res.Sold).To(Equal(vari.Sold))
			Expect(res.Taxable).To(Equal(vari.Taxable))
		})
	})

	Context("Put variant", func() {
		vari := new(variant.Variant)
		res := new(variant.Variant)
		req := new(variant.Variant)

		Before(func() {
			// Create product
			prod := product.Fake(db)
			prod.MustCreate()

			// Create variant
			vari = variant.Fake(db, prod.Id())
			vari.MustCreate()

			// Create variant request
			req = variant.Fake(db, prod.Id())

			// Update variant
			cl.Put("/variant/"+vari.Id(), req, res)
		})

		It("Should put variant", func() {
			Expect(res.Id_).To(Equal(vari.Id()))
			Expect(res.ProductId).To(Equal(req.ProductId))
			Expect(res.SKU).To(Equal(req.SKU))
			Expect(res.Available).To(Equal(req.Available))
			Expect(res.Name).To(Equal(req.Name))
			Expect(res.Inventory).To(Equal(req.Inventory))
			Expect(res.Sold).To(Equal(req.Sold))
			Expect(res.Taxable).To(Equal(req.Taxable))
		})
	})

	Context("Delete variant", func() {
		res := ""

		Before(func() {
			prod := product.Fake(db)
			prod.MustCreate()
			req := variant.Fake(db, prod.Id())
			req.MustCreate()

			cl.Delete("/variant/" + req.Id())
			res = req.Id()
		})

		It("Should delete variants", func() {
			vari := variant.New(db)
			err := vari.GetById(res)
			Expect(err).ToNot(BeNil())
		})
	})
})
