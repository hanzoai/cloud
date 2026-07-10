package test

import (
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/fixtures"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/util/gincontext"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"

	"github.com/hanzoai/cloud/clients/commerce/thirdparty/authorizenet"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var (
	c      *gin.Context
	ctx    ae.Context
	db     *datastore.Datastore
	client *authorizenet.Client
	token string
)

func Test(t *testing.T) {
	Setup("thirdparty/authorizenet", t)
}

var _ = BeforeSuite(func() {
	ctx = ae.NewContext()
	c = gincontext.New(ctx)
	db = datastore.New(c)
	log.Warn("Before Suite")
	org := fixtures.Organization(c).(*organization.Organization)
	loginId := org.AuthorizeNet.Sandbox.LoginId
	transactionKey := org.AuthorizeNet.Sandbox.TransactionKey
	key := org.AuthorizeNet.Sandbox.Key
	client = authorizenet.New(ctx, loginId, transactionKey, key, true)
})

var _ = AfterSuite(func() {
	ctx.Close()
})

/*
* API Login Id: 
* Transaction Key: 
* Key: Simon
*/
