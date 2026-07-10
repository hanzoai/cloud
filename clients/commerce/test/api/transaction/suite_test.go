package test

import (
	"net/http"
	"testing"

	transactionApi "github.com/hanzoai/cloud/clients/commerce/api/transaction"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/fixtures"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/util/gincontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ginclient"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("api/transaction", t)
}

var (
	ctx          ae.Context
	db           *datastore.Datastore
	org          *organization.Organization
	cl           *ginclient.Client
	accessToken  string
	pAccessToken string
)

// Setup test context
var _ = BeforeSuite(func() {
	// Create new test context
	ctx = ae.NewContext()

	// Mock gin context that we can use with fixtures
	c := gincontext.New(ctx)

	// Run default fixtures to setup organization and default store
	org = fixtures.Organization(c).(*organization.Organization)
	tok1, _ := org.GetTokenByName("test-secret-key")
	tok2, _ := org.GetTokenByName("test-published-key")
	org.MustUpdate()

	accessToken = tok1.String
	pAccessToken = tok2.String

	// Save namespaced db
	db = datastore.New(org.Namespaced(ctx))

	// Create client so we can make requests
	cl = ginclient.New(ctx)

	// Set authorization header for subsequent requests
	cl.Defaults(func(r *http.Request) {
		r.Header.Set("Authorization", accessToken)
	})

	cl.IgnoreErrors(true)

	// Add API routes to client
	transactionApi.Route(cl.Router)
})

// Tear-down test context
var _ = AfterSuite(func() {
	ctx.Close()
})
