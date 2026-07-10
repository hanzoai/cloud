package test

import (
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	// "github.com/hanzoai/cloud/clients/commerce/models/fixtures"
	"github.com/hanzoai/cloud/clients/commerce/models/wallet"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/ethereum"
	"github.com/hanzoai/cloud/clients/commerce/util/gincontext"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var (
	ctx    ae.Context
	c      *gin.Context
	db     *datastore.Datastore
	client ethereum.Client
	w      *wallet.Wallet
)

func Test(t *testing.T) {
	Setup("thirdparty/ethereum", t)
}

// Can't actually test without mocking because we can't regenerate wallets

var _ = BeforeSuite(func() {
	ctx = ae.NewContext()
	c = gincontext.New(ctx)
	db = datastore.New(ctx)

	// w = fixtures.PlatformWallet(c).(*wallet.Wallet)

	client = ethereum.New(ctx, config.Ethereum.TestNetNodes[0])
})

var _ = AfterSuite(func() {
	ctx.Close()
})
