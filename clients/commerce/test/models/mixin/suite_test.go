package test

import (
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/fixtures"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/util/gincontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

func Test(t *testing.T) {
	Setup("models/mixin", t)
}

var (
	ctx ae.Context
	db  *datastore.Datastore
)

// Setup test context
var _ = BeforeSuite(func() {
	ctx = ae.NewContext()
	db = datastore.New(ctx)

	// Mock gin context that we can use with fixtures
	c := gincontext.New(ctx)

	org := fixtures.Organization(c).(*organization.Organization)
	org.MustUpdate()
})

// Tear-down test context
var _ = AfterSuite(func() {
	ctx.Close()
})
