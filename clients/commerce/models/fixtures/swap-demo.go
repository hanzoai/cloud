package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/models/wallet"
	"github.com/hanzoai/cloud/clients/commerce/types/email"
	"github.com/hanzoai/cloud/clients/commerce/types/integration"
	"github.com/hanzoai/cloud/clients/commerce/types/website"
	// "github.com/hanzoai/cloud/clients/commerce/models/webhook"
)

var _ = New("swap-demo", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "swap-demo"
	org.GetOrCreate("Name=", org.Name)

	u := user.New(db)
	u.Email = "ico@hanzo.ai"
	u.GetOrCreate("Email=", u.Email)
	u.FirstName = "ICO"
	u.LastName = "User"
	u.Organizations = []string{org.Id()}
	u.PasswordHash, _ = password.Hash("dWcSGthgDpT5B73p")
	u.Put()

	u2 := user.New(db)
	u2.Email = "phil@ar.ca"
	u2.GetOrCreate("Email=", u2.Email)
	u2.FirstName = "Phil"
	u2.LastName = "Liu"
	u2.Organizations = []string{org.Id()}
	u2.PasswordHash, _ = password.Hash("ZIMwE8Z6q8Wo0otC")
	u2.Put()

	org.FullName = "TA DEMO"
	org.Owners = []string{u.Id(), u2.Id()}
	org.Websites = []website.Website{website.Website{Type: website.Production, Url: "https://ico.hanzo.ai"}}
	org.SecretKey = []byte("XzJn6Asyd9ZVSuaCDHjxj3tuhAb6FPLnzZ5VU9Md6VwsMrnCHrkcz8ZBBxqMURJD")

	org.Fees.Card.Flat = 50
	org.Fees.Card.Percent = 0.05
	org.Fees.Affiliate.Flat = 30
	org.Fees.Affiliate.Percent = 0.30
	org.Fees.Ethereum.Flat = 0 // 500000
	org.Fees.Ethereum.Percent = 0.06

	// Email configuration
	// org.Mandrill.APIKey = ""

	org.Email.Enabled = true
	org.Email.Defaults.From = email.Email{
		Name:    "Hanzo ICO",
		Address: "hi@hanzo.ai",
	}

	org.SignUpOptions.ImmediateLogin = true
	org.SignUpOptions.AccountsEnabledByDefault = true

	eth := &integration.Integration{
		Type:    integration.EthereumType,
		Enabled: true,
		Ethereum: integration.Ethereum{
			Address:     "0xf8f59f0269c4f6d7b5c5ab98d70180eaa0c7507e",
			TestAddress: "0xf8f59f0269c4f6d7b5c5ab98d70180eaa0c7507e",
		},
	}

	if len(org.Integrations.FilterByType(eth.Type)) == 0 {
		org.Integrations = org.Integrations.MustAppend(eth)
	}

	// Save org into default namespace
	org.MustUpdate()

	w := wallet.New(db)
	w.Id_ = "swap-demo-wallet"
	w.UseStringKey = true
	w.GetOrCreate("Id_=", "swap-demo-wallet")

	if a, _ := w.GetAccountByName("swap-demo-test"); a == nil {
		if _, err := w.CreateAccount("swap-demo-test", blockchains.EthereumRopstenType, []byte("G9wPCV39uaXWUW5SUSCzjTEEUA2pbzmZaX27pCYndJYarALD2pNUyNKEgkGewr3p")); err != nil {
			panic(err)
		}
	}

	if a, _ := w.GetAccountByName("swap-demo"); a == nil {
		if _, err := w.CreateAccount("swap-demo", blockchains.EthereumType, []byte("G9wPCV39uaXWUW5SUSCzjTEEUA2pbzmZaX27pCYndJYarALD2pNUyNKEgkGewr3p")); err != nil {
			panic(err)
		}
	}

	// nsDb := datastore.New(org.Namespaced(c))

	// wh := webhook.New(nsDb)
	// wh.Name = "picatic-proxy"
	// wh.GetOrCreate("Name=", "picatic-proxy")

	// if wh.AccessToken == "" {
	// 	wh.AccessToken = ""
	// 	wh.Live = true
	// 	wh.Url = "http://35.188.46.251/webhook"
	// 	wh.Events = webhook.Events{
	// 		"order.paid": true,
	// 	}
	// 	wh.Enabled = true
	// 	wh.MustUpdate()
	// }

	return org
})
