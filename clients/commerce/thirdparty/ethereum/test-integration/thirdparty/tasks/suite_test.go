package test

import (
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/fixtures"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/models/wallet"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/ethereum"
	"github.com/hanzoai/cloud/clients/commerce/util/gincontext"
	"github.com/hanzoai/cloud/clients/commerce/util/rand"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"
)

var (
	ctx  ae.Context
	c    *gin.Context
	db   *datastore.Datastore
	nsDb *datastore.Datastore
	ord  *order.Order
	org  *organization.Organization
	usr  *user.User
	w    *wallet.Wallet
	pw   *wallet.Wallet
)

func Test(t *testing.T) {
	Setup("thirdparty/ethereum/tasks", t)
}

var _ = BeforeSuite(func() {
	ctx = ae.NewContext()
	c = gincontext.New(ctx)
	db = datastore.New(ctx)

	org = fixtures.Organization(c).(*organization.Organization)
	pw = fixtures.PlatformWallet(c).(*wallet.Wallet)

	nsCtx := org.Namespaced(ctx)
	nsDb = datastore.New(nsCtx)

	ord = order.New(nsDb)
	ord.Currency = currency.ETH
	ord.Total = 123 * 1e6
	ord.WalletPassphrase = rand.SecretKey()
	ord.Test = true

	usr = user.New(nsDb)
	usr.FirstName = "Test"
	usr.LastName = "User"
	usr.MustCreate()

	ord.UserId = usr.Id()

	w, _ = ord.GetOrCreateWallet(ord.Datastore())
	w.CreateAccount("Receiver Account", blockchains.EthereumRopstenType, []byte(ord.WalletPassphrase))

	ord.MustCreate()

	ethereum.Test(true)
})

var _ = AfterSuite(func() {
	ctx.Close()
})
