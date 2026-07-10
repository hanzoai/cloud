package test

import (
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/product"
	"github.com/hanzoai/cloud/clients/commerce/util/hashid"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

// productList mirrors the rest.Pagination envelope the console Store pages read.
type productList struct {
	Count  int               `json:"count"`
	Models []product.Product `json:"models"`
}

// GET /product must return the caller's real datastore rows (regression: the
// generic rest list handler routed through a dead search backend and always
// returned an empty page) AND must never cross tenants — a caller lists ONLY
// its own org's rows, scoped by the namespace the auth middleware resolves from
// its token. `db`, `ctx`, and `cl` come from the suite (org namespace: suchtees).
var _ = Describe("product list (datastore-backed, per-org isolation)", func() {
	Context("GET /product", func() {
		var mineID string
		var theirID string

		Before(func() {
			// A product owned by the authenticated suite org (namespace: suchtees).
			mine := product.Fake(db)
			mine.MustCreate()
			mineID = mine.Id()

			// A product owned by a DIFFERENT org, written to its OWN namespace —
			// exactly what organization.Namespaced() produces for another tenant.
			const otherNS = "isolation-org-b"
			hashid.RegisterNamespace(otherNS, 987654321)
			otherDB := datastore.New(nscontext.WithNamespace(ctx, otherNS))
			theirs := product.Fake(otherDB)
			theirs.MustCreate()
			theirID = theirs.Id()

			// Non-vacuous guard: the other tenant's product really is stored in
			// its own namespace (GetById is namespace-scoped), so "excludes
			// theirs" below proves isolation rather than an empty result.
			Expect(product.New(otherDB).GetById(theirID)).To(BeNil())
		})

		It("returns the caller's real rows (no longer empty)", func() {
			res := new(productList)
			cl.Get("/product", res)
			Expect(res.Count).To(BeNumerically(">", 0))
			Expect(len(res.Models)).To(BeNumerically(">", 0))
		})

		It("includes the caller's own product", func() {
			res := new(productList)
			cl.Get("/product", res)

			found := false
			for i := range res.Models {
				if res.Models[i].Id_ == mineID {
					found = true
				}
			}
			Expect(found).To(BeTrue())
		})

		It("never returns another org's product (per-tenant isolation)", func() {
			res := new(productList)
			cl.Get("/product", res)

			for i := range res.Models {
				Expect(res.Models[i].Id_).ToNot(Equal(theirID))
			}
		})
	})
})
