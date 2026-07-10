package test

import (
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/clients/commerce/api/organization/newroutes"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/middleware"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/fixtures"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/models/wallet"
	"github.com/hanzoai/cloud/clients/commerce/util/gincontext"
	"github.com/hanzoai/cloud/clients/commerce/util/nscontext"
	//"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/util/permission"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ae"
	"github.com/hanzoai/cloud/clients/commerce/util/test/ginclient"

	. "github.com/hanzoai/cloud/clients/commerce/util/test/ginkgo"

	organizationApi "github.com/hanzoai/cloud/clients/commerce/api/organization"
)

func Test(t *testing.T) {
	Setup("api/account", t)
}

var (
	ctx         ae.Context
	cl          *ginclient.Client
	accessToken string
	db          *datastore.Datastore
	org         *organization.Organization
	u           *user.User
	bcDb        *datastore.Datastore
)

// Setup test context
var _ = BeforeSuite(func() {
	adminRequired := middleware.TokenRequired(permission.Admin)

	// Create a new app engine context
	ctx = ae.NewContext()

	// Create mock gin context that we can use with fixtures
	c := gincontext.New(ctx)

	// Run fixtures
	u = fixtures.User(c).(*user.User)
	org = fixtures.Organization(c).(*organization.Organization)

	// Setup client and add routes for account API tests.
	cl = ginclient.New(ctx)
	organizationApi.Route(cl.Router, adminRequired)

	// Create organization for tests, accessToken
	accessToken, _ := org.GetTokenByName("test-secret-key")
	err := org.Put()
	Expect(err).NotTo(HaveOccurred())

	// Set authorization header for subsequent requests
	cl.Defaults(func(r *http.Request) {
		r.Header.Set("Authorization", accessToken.String)
	})

	// Save namespaced db
	db = datastore.New(org.Namespaced(ctx))
	usr := user.New(db)
	usr.Email = "dev@hanzo.ai"
	usr.SetPassword("Z0rd0N")
	usr.Enabled = true
	usr.MustPut()

	usr2 := user.New(db)
	usr2.Email = "dev@hanzo.ai"
	usr2.SetPassword("ilikedragons")
	usr2.Enabled = false
	usr2.MustPut()

	usr3 := user.New(db)
	usr3.FirstName = "Z"
	usr3.LastName = "T"
	usr3.Email = "zack@taylor.edu"
	usr3.Enabled = false
	usr3.MustPut()

	usr4 := user.New(db)
	usr4.FirstName = "Z"
	usr4.LastName = "T"
	usr4.Email = "dev@hanzo.ai"
	usr4.SetPassword("blackisthenewred")
	usr4.Enabled = true
	usr4.MustPut()

	ctx := ae.NewContext()
	nsCtx := nscontext.WithNamespace(ctx, "_blockchains")
	bcDb = datastore.New(nsCtx)
})

// Tear-down test context
var _ = AfterSuite(func() {
	ctx.Close()
})

type retrieveWalletRes struct {
	wallet.WalletHolder
}

type createAccountRes struct {
	wallet.Account
}

type retrieveAccountRes struct {
	wallet.Account
}
type payFromAccountRes struct {
	TransactionId string
}
type loginRes struct {
	Token string `json:"token"`
}
type getWithdrawableAccountsRes struct {
	newroutes.GetWithdrawableAccountsRes
}

var _ = Describe("organization", func() {
	Context("Create", func() {
		It("Should create wallet account", func() {
			req := `{
				"name": "test-wallet-account-1",
				"blockchain": "ethereum"
			}`
			res := createAccountRes{}

			cl.Post("/c/organization/"+org.Id()+"/wallet/account", req, &res)
		})
	})

	Context("Get", func() {
		It("Should retrieve wallet", func() {
			res := retrieveWalletRes{}

			cl.Get("/c/organization/"+org.Id()+"/wallet", &res)
		})
		It("Should retrieve created wallet account", func() {
			orgWallet, _ := org.GetOrCreateWallet(org.Datastore())
			_, err := orgWallet.CreateAccount("test-wallet-account-2", blockchains.EthereumType, []byte("shamma-lamma-ding-dong"))
			Expect(err).ToNot(HaveOccurred())
			org.MustUpdate()

			resRetrieve := retrieveAccountRes{}

			cl.Get("/c/organization/"+org.Id()+"/wallet/account/test-wallet-account-2", &resRetrieve)
		})
		It("Should retrieve withdrawable wallet accounts", func() {
			orgWallet, _ := org.GetOrCreateWallet(org.Datastore())
			_, err := orgWallet.CreateAccount("test-wallet-account-3", blockchains.EthereumType, []byte("shamma-lamma-ding-dong"))
			Expect(err).ToNot(HaveOccurred())

			a, err := orgWallet.CreateAccount("test-wallet-account-4", blockchains.EthereumType, []byte("shamma-lamma-ding-dong"))
			a.Withdrawable = true
			Expect(err).ToNot(HaveOccurred())

			a, err = orgWallet.CreateAccount("test-wallet-account-5", blockchains.EthereumRopstenType, []byte("shamma-lamma-ding-dong"))
			a.Withdrawable = true
			Expect(err).ToNot(HaveOccurred())

			a, err = orgWallet.CreateAccount("test-wallet-account-6", blockchains.BitcoinType, []byte("shamma-lamma-ding-dong"))
			a.Withdrawable = true
			Expect(err).ToNot(HaveOccurred())

			a, err = orgWallet.CreateAccount("test-wallet-account-7", blockchains.BitcoinTestnetType, []byte("shamma-lamma-ding-dong"))
			a.Withdrawable = true
			Expect(err).ToNot(HaveOccurred())

			orgWallet.MustUpdate()
			org.MustUpdate()

			resRetrieve := getWithdrawableAccountsRes{}
			cl.Get("/organization/publicwithdrawableaccounts", &resRetrieve)
			Expect(len(resRetrieve.Accounts)).To(Equal(6))
			// It starts with 2 accounts
			Expect(resRetrieve.Accounts[2]).To(Equal(newroutes.AccountNameRes{Name: "test-wallet-account-4", Type: blockchains.EthereumType, Currency: currency.ETH}))
			Expect(resRetrieve.Accounts[3]).To(Equal(newroutes.AccountNameRes{Name: "test-wallet-account-5", Type: blockchains.EthereumRopstenType, Currency: currency.ETH}))
			Expect(resRetrieve.Accounts[4]).To(Equal(newroutes.AccountNameRes{Name: "test-wallet-account-6", Type: blockchains.BitcoinType, Currency: currency.BTC}))
			Expect(resRetrieve.Accounts[5]).To(Equal(newroutes.AccountNameRes{Name: "test-wallet-account-7", Type: blockchains.BitcoinTestnetType, Currency: currency.BTC}))
		})
		/*It("Should make ordered payment", func() {
			req := `{
				"name": "test-wallet-account",
				"blockchain": "ethereum-ropsten"
			}`
			req2 := `{
				"name": "test-wallet-account",
				"to": "0x123f681646d4a755815f9cb19e1acc8565a0c2ac",
				"amount": "234923838"
			}`
			res := createAccountRes{}
			res2 := payFromAccountRes{}

			cl.Post("/c/organization/"+org.Id()+"/wallet/createaccount", req, &res)
			cl.Post("/c/organization/"+org.Id()+"/wallet/pay", req2, &res2)
		})*/
	})
})
