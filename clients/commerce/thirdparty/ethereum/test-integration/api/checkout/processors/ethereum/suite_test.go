package integration

import (
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/fixtures"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/models/tokensale"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/ethereum"
	"github.com/hanzoai/cloud/clients/commerce/util/gincontext"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ginclient"

	checkoutApi "github.com/hanzoai/cloud/clients/commerce/api/checkout"
	storeApi "github.com/hanzoai/cloud/clients/commerce/api/store"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("api/checkout/integration/ethereum", t)
}

var (
	accessToken string
	cl          *ginclient.Client
	ctx         ae.Context
	db          *datastore.Datastore
	org         *organization.Organization
	stor        *store.Store
	u           *user.User
	ts          *tokensale.TokenSale
)

// Setup test context
var _ = BeforeSuite(func() {
	// Set EthereumClient to Test Mode
	ethereum.Test(true)

	adminRequired := middleware.TokenRequired(permission.Admin)

	ctx = ae.NewContext()

	// Mock gin context that we can use with fixtures
	c := gincontext.New(ctx)
	u = fixtures.User(c).(*user.User)
	org = fixtures.Organization(c).(*organization.Organization)
	fixtures.PlatformWallet(c)

	// Save namespaced db
	db = datastore.New(org.Namespaced(ctx))

	stor = fixtures.Store(c).(*store.Store)
	ts = tokensale.Fake(db)
	ts.MustCreate()

	// Setup client and add routes for payment API tests.
	cl = ginclient.New(ctx)
	cl.IgnoreErrors(true)
	checkoutApi.Route(cl.Router, adminRequired)
	storeApi.Route(cl.Router, adminRequired)

	// Create organization for tests, accessToken
	accessToken, _ := org.GetTokenByName("test-secret-key")
	err := org.Put()
	Expect(err).NotTo(HaveOccurred())

	// Set authorization header for subsequent requests
	cl.Defaults(func(r *http.Request) {
		r.Header.Set("Authorization", accessToken.String)
	})
})

// Tear-down test context
var _ = AfterSuite(func() {
	ctx.Close()
})
