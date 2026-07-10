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
	"github.com/hanzoai/cloud/clients/commerce/types/website"
	// "github.com/hanzoai/cloud/clients/commerce/models/webhook"
)

var _ = New("palladium46", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "palladium46"
	org.GetOrCreate("Name=", org.Name)

	u := user.New(db)
	u.Email = "hanzo@palladium46.com"
	u.GetOrCreate("Email=", u.Email)
	u.FirstName = "Palladium"
	u.LastName = "46"
	u.Organizations = []string{org.Id()}
	u.PasswordHash, _ = password.Hash("65l#kMVtsL^9")
	u.Put()

	org.FullName = "Palladium46"
	org.Owners = []string{u.Id()}
	org.Websites = []website.Website{website.Website{Type: website.Production, Url: "https://palladium46.com"}}
	org.SecretKey = []byte("fAW5yilqBpvOpUElgxMzjntj9Hq4vtCi9P9X6IA974z348ayUEfkkeJRBSSnwyMK")

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
		Name:    "Palladium46",
		Address: "hi@palladium46.com",
	}

	// Save org into default namespace
	org.MustUpdate()

	w := wallet.New(db)
	w.Id_ = "palladiu46-wallet"
	w.UseStringKey = true
	w.GetOrCreate("Id_=", "palladium46-wallet")

	if a, _ := w.GetAccountByName("palladium46-test"); a == nil {
		if _, err := w.CreateAccount("palladium46-test", blockchains.EthereumRopstenType, []byte("G9wPCV39uaXWUW5SUSCzjTEEUA2pbzmZaX27pCYndJYarALD2pNUyNKEgkGewr3p")); err != nil {
			panic(err)
		}
	}

	if a, _ := w.GetAccountByName("palladium46"); a == nil {
		if _, err := w.CreateAccount("palladium46", blockchains.EthereumType, []byte("G9wPCV39uaXWUW5SUSCzjTEEUA2pbzmZaX27pCYndJYarALD2pNUyNKEgkGewr3p")); err != nil {
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
