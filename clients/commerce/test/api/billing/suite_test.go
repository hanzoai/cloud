package test

import (
	"net/http"
	"os"
	"testing"

	billingApi "github.com/hanzoai/cloud/clients/commerce/api/billing"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/fixtures"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/transaction"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/util/gincontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ginclient"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

// serviceToken is the internal service-to-service secret (cloud-api → commerce).
// This suite drives the FULL billing surface, which since C1 includes money-MINT
// routes (credit-grants, deposit) that are PlatformOnly — reachable ONLY by the
// verified service token or a platform SuperAdmin, never a plain org-admin
// access token. The suite therefore authenticates as that service principal (the
// real production caller for these routes: cloud-api). It still resolves the SAME
// fixture org ("suchtees") via X-Org-Id, so every non-mint spec is unaffected.
const serviceToken = "test-commerce-service-token-billing-suite"

func Test(t *testing.T) {
	Setup("api/billing", t)
}

var (
	ctx         ae.Context
	db          *datastore.Datastore
	org         *organization.Organization
	cl          *ginclient.Client
	accessToken string
)

// Setup test context
var _ = BeforeSuite(func() {
	// Create new test context
	ctx = ae.NewContext()

	// Mock gin context for fixtures
	c := gincontext.New(ctx)

	// Run default fixtures to setup organization
	org = fixtures.Organization(c).(*organization.Organization)
	tok, _ := org.GetTokenByName("test-secret-key")
	org.MustUpdate()

	accessToken = tok.String

	// Save namespaced db
	db = datastore.New(org.Namespaced(ctx))

	// Authenticate the suite as the internal service token (the production caller
	// for the money-mint billing routes since C1). TokenRequired verifies the
	// bearer against COMMERCE_SERVICE_TOKEN, marks the request a verified service
	// caller (→ MayMintMoney true, PlatformOnly passes), and resolves the org from
	// X-Org-Id — pinned to the fixture org so all specs stay in one namespace.
	os.Setenv("COMMERCE_SERVICE_TOKEN", serviceToken)

	// Create client
	cl = ginclient.New(ctx)

	// Authenticate the way cloud-api actually calls commerce billing: the
	// internal service token (Bearer COMMERCE_SERVICE_TOKEN) + the target org
	// (X-Org-Id). TokenRequired's service-token branch verifies the bearer,
	// resolves the fixture org, and stamps the service-token marker that
	// middleware.PlatformOnly requires for the money-MINT routes. A per-org
	// access token carries only org-level Admin and is (correctly) 403 on those
	// routes — an org owner must never be able to self-mint spendable balance.
	os.Setenv("COMMERCE_SERVICE_TOKEN", serviceToken)
	cl.Defaults(func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+serviceToken)
		r.Header.Set("X-Org-Id", org.Name)
	})

	cl.IgnoreErrors(true)

	// Add billing API routes to client
	billingApi.Route(cl.Router)
})

// Tear-down test context
var _ = AfterSuite(func() {
	os.Unsetenv("COMMERCE_SERVICE_TOKEN")
	ctx.Close()
})

// seedCharge writes a Withdraw (charge) to the fixture org's ledger and returns
// its id — the thing a refund reverses. It mirrors the money model the Refund
// handler validates after the H1 hardening (an original that exists, is a
// Withdraw, and is owned by the subject); see api/billing/refund_h1_test.go's
// seedCharge. A refund of a Deposit is (correctly) rejected — refunding a credit
// would double it — so the refund test must reverse a real charge, not a deposit.
func seedCharge(subject string, cents int64) string {
	tr := transaction.New(db)
	tr.Type = transaction.Withdraw
	tr.DestinationId = subject
	tr.DestinationKind = "iam-user"
	tr.Currency = currency.USD
	tr.Amount = currency.Cents(cents)
	tr.MustCreate()
	return tr.Id()
}
