package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/util/hashid"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/shippingrates"
	"github.com/hanzoai/cloud/clients/commerce/models/store"
	"github.com/hanzoai/cloud/clients/commerce/models/taxrates"
	"github.com/hanzoai/cloud/clients/commerce/models/types/georate"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/types/website"

	. "github.com/hanzoai/cloud/clients/commerce/models/types/analytics"
)

var Organization = New("organization", func(c *gin.Context) *organization.Organization {
	BlockchainNamespace(c)

	db := datastore.New(c)

	// Such tees owner &operator
	usr := User(c).(*user.User)

	// Our fake T-shirt company
	org := organization.New(db)
	org.Name = "suchtees"
	org.SecretKey = []byte("prettyprettyteesplease")
	org.GetOrCreate("Name=", org.Name)

	org.FullName = "Such Tees, Inc."
	org.Owners = []string{usr.Id()}
	org.Websites = []website.Website{website.Website{Type: website.Production, Url: "http://suchtees.com"}}
	org.AddDefaultTokens()

	org.AuthorizeNet.Sandbox.LoginId = ""
	org.AuthorizeNet.Sandbox.TransactionKey = ""
	org.AuthorizeNet.Sandbox.Key = "Simon"

	// Ethereum
	org.Ethereum.Address = "0xf2fccc0198fc6b39246bd91272769d46d2f9d43b"
	org.Bitcoin.Address = ""
	org.Bitcoin.TestAddress = "mrPFGX5ViUZk2s8i5soBCkrFVzRwngK8DQ"

	org.Paypal.ConfirmUrl = "http://hanzo.ai"
	org.Paypal.CancelUrl = "http://hanzo.ai"

	org.Paypal.Live.Email = "dev@hanzo.ai"
	org.Paypal.Live.SecurityUserId = "dev@hanzo.ai"
	org.Paypal.Live.ApplicationId = "APP-80W284485P519543T"
	org.Paypal.Live.SecurityPassword = ""
	org.Paypal.Live.SecuritySignature = ""

	org.Paypal.Test.Email = "dev@hanzo.ai"
	org.Paypal.Test.SecurityUserId = "dev@hanzo.ai"
	org.Paypal.Test.ApplicationId = "APP-80W284485P519543T"
	org.Paypal.Test.SecurityPassword = ""
	org.Paypal.Test.SecuritySignature = ""

	org.WalletPassphrase = "1234"

	{
		w, _ := org.GetOrCreateWallet(org.Datastore())
		a1, _ := w.CreateAccount("Test Ethereum", blockchains.EthereumRopstenType, []byte(org.WalletPassphrase))
		a1.Withdrawable = true
		a2, _ := w.CreateAccount("Test Bitcoin", blockchains.BitcoinTestnetType, []byte(org.WalletPassphrase))
		a2.Withdrawable = true
		w.MustUpdate()
	}

	// Add default access tokens
	// org.AddDefaultTokens()
	// log.Debug("Adding tokens: %v", org.Tokens)

	// Add default analytics config
	integrations := []Integration{
		Integration{
			Type: "facebook-pixel",
			Id:   "920910517982389",
		},
		Integration{
			Type: "google-analytics",
			Id:   "UA-65099214-1",
		},
	}
	org.Analytics = Analytics{Integrations: integrations}

	// Save org into default namespace
	org.MustPut()

	// Register namespace mapping so hashid can encode/decode keys for this org.
	// Must happen before creating any models in the org's namespace (taxrates, etc.)
	hashid.RegisterNamespace(org.Name, org.Key().IntID())

	// Retrofit existing thing
	if org.DefaultStore == "" {
		nsdb := datastore.New(org.Namespaced(org.Context()))

		stor := store.New(nsdb)
		stor.GetOrCreate("Name=", "Default")
		stor.Name = "Default"
		stor.Currency = org.Currency
		stor.MustUpdate()

		trs := taxrates.New(nsdb)
		trs.GetOrCreate("StoreId=", stor.Id())
		trs.StoreId = stor.Id()
		trs.MustCreate()

		srs := shippingrates.New(nsdb)
		srs.GetOrCreate("StoreId=", stor.Id())
		srs.StoreId = stor.Id()
		srs.MustCreate()

		org.DefaultStore = stor.Id()
		org.MustUpdate()
	}

	stor, _ := org.GetDefaultStore()

	trs, _ := stor.GetTaxRates()
	trs.GeoRates = []taxrates.GeoRate{
		taxrates.GeoRate{
			GeoRate: georate.New(
				"US",
				"MO",
				"",
				"64108",
				0,
				0,
				0.08475,
				0,
			),
		},
		taxrates.GeoRate{
			GeoRate: georate.New(
				"US",
				"MO",
				"",
				"",
				0,
				0,
				0.04225,
				0,
			),
		},
	}

	trs.MustUpdate()

	srs, _ := stor.GetShippingRates()
	srs.GeoRates = []shippingrates.GeoRate{
		shippingrates.GeoRate{
			GeoRate: georate.New(
				"US",
				"",
				"",
				"",
				0,
				0,
				0,
				499,
			),
		},
		shippingrates.GeoRate{
			GeoRate: georate.New(
				"",
				"",
				"",
				"",
				0,
				0,
				0,
				999,
			),
		},
	}

	srs.MustUpdate()

	// Add org to user and also save
	usr.Organizations = []string{org.Id()}
	usr.MustPut()
	return org
})
