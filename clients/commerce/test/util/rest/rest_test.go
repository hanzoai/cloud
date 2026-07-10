package test

import (
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/util/bit"
	"github.com/hanzoai/cloud/clients/commerce/util/rest"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ginclient"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("util/rest", t)
}

var (
	ctx  ae.Context
	tok1 string
	tok2 string
)

const (
	Perm1 bit.Mask = 1 << iota // 1 << 0 which is 00000001
	Perm2
	Perm3
)

// Setup test context
var _ = BeforeSuite(func() {
	ctx = ae.NewContext()

	// Setup organization so Authorization middleware works
	db := datastore.New(ctx)
	org := organization.New(db)
	tok1 = org.AddToken("tok1", Perm1)
	tok2 = org.AddToken("tok2", Perm2|Perm3)
	err := org.Put()
	Expect(err).NotTo(HaveOccurred())
})

// Tear-down test context
var _ = AfterSuite(func() {
	ctx.Close()
})

var _ = Describe("New", func() {
	It("Should create a new Rest object with CRUD routes", func() {
		client := ginclient.New(ctx)

		// Create routes for Model
		r := rest.New(Model{})
		r.Permissions = rest.Permissions{
			"get":  []bit.Mask{Perm1, Perm2 | Perm3},
			"list": []bit.Mask{Perm1, Perm2 | Perm3},
		}
		r.Route(client.Router, middleware.TokenRequired())

		// Should not be authorized
		client.Get("/test-model", nil, 401)

		// Set authorization header for subsequent requests
		client.Defaults(func(r *http.Request) {
			r.Header.Set("Authorization", tok1)
		})

		// Get should work ok
		client.Get("/test-model", nil, 200)

		// Should 404
		client.Get("/test-model2", nil, 404)

		// Should work with more complex token
		client.Defaults(func(r *http.Request) {
			r.Header.Set("Authorization", tok2)
		})

		// Get should work ok
		client.Get("/test-model", nil, 200)

		// Should 404
		client.Get("/test-model2", nil, 404)
	})
})
