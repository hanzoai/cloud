package fixtures

import (
	// "math/rand"
	// "time"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	// "github.com/hanzoai/cloud/clients/commerce/demo/disclosure"
	// "github.com/hanzoai/cloud/clients/commerce/demo/tokentransaction"
	// "github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/types/email"
	"github.com/hanzoai/cloud/clients/commerce/types/integration"
	"github.com/hanzoai/cloud/clients/commerce/types/website"
	// "github.com/hanzoai/cloud/clients/commerce/util/fake"
)

var SECDemo = New("sec-demo", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "sec-demo"
	org.GetOrCreate("Name=", org.Name)

	u := user.New(db)
	u.Email = "sec@hanzo.ai"
	u.GetOrCreate("Email=", u.Email)
	u.FirstName = "SEC"
	u.LastName = "User"
	u.Organizations = []string{org.Id()}
	u.PasswordHash, _ = password.Hash("secdemo2")
	u.Put()

	org.FullName = "SEC DEMO"
	org.Owners = []string{u.Id()}
	org.Websites = []website.Website{website.Website{Type: website.Production, Url: "https://sec.hanzo.ai"}}
	org.SecretKey = []byte("XzJn6Asyd9ZVSuaCDHjxj3tuhAb6FPLnzZ5VU9Md6VwsMrnCHrkcz8ZBBxqMURJD")
	org.AddDefaultTokens()

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
		Name:    "Hanzo",
		Address: "info@hanzo.ai",
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

	// w := wallet.New(db)
	// w.Id_ = "sec-demo-wallet"
	// w.UseStringKey = true
	// w.GetOrCreate("Id_=", "sec-demo-wallet")

	// if a, _ := w.GetAccountByName("sec-demo-test"); a == nil {
	// 	if _, err := w.CreateAccount("sec-demo-test", blockchains.EthereumRopstenType, []byte("G9wPCV39uaXWUW5SUSCzjTEEUA2pbzmZaX27pCYndJYarALD2pNUyNKEgkGewr3p")); err != nil {
	// 		panic(err)
	// 	}
	// }

	// if a, _ := w.GetAccountByName("sec-demo"); a == nil {
	// 	if _, err := w.CreateAccount("sec-demo", blockchains.EthereumType, []byte("G9wPCV39uaXWUW5SUSCzjTEEUA2pbzmZaX27pCYndJYarALD2pNUyNKEgkGewr3p")); err != nil {
	// 		panic(err)
	// 	}
	// }

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

	// nsDb := datastore.New(org.Namespaced(c))

	// users := make([]*user.User, 0)

	// for i := 0; i < 103; i++ {
	// 	usr := user.New(nsDb)
	// 	usr.Email = fake.EmailAddress()
	// 	usr.GetOrCreate("Email=", usr.Email)

	// 	usr.FirstName = fake.FirstName()
	// 	usr.LastName = fake.LastName()
	// 	usr.PasswordHash, _ = password.Hash("secdemo")

	// 	usr.KYC.Phone = fake.Phone()
	// 	usr.KYC.Birthdate = time.Date(fake.Year(1942, 2000), time.Month(fake.MonthNum()), fake.Day(), 0, 0, 0, 0, nil)
	// 	usr.KYC.Gender = fake.Gender()
	// 	usr.KYC.Address.Name = usr.FirstName + " " + usr.LastName
	// 	usr.KYC.Address.Line1 = fake.StreetAddress()
	// 	usr.KYC.Address.City = fake.City()
	// 	usr.KYC.Address.State = fake.StateAbbrev()
	// 	usr.KYC.Address.PostalCode = fake.Zip()
	// 	usr.KYC.Address.Country = "US"
	// 	usr.KYC.TaxId = fake.TaxID()
	// 	usr.KYC.WalletAddresses = []string{fake.EOSAddress(), fake.EthereumAddress()}
	// 	usr.MustPut()
	// 	usr.MustUpdate()

	// 	users = append(users, usr)
	// }

	// for i := 0; i < 420; i++ {
	// 	tr := tokentransaction.New(nsDb)

	// 	usr := users[rand.Intn(100)]
	// 	usr2 := users[rand.Intn(100)]

	// 	log.Warn("HI %v, %v", usr.FirstName, usr.LastName, c)

	// 	if rand.Float64() > 0.7 {
	// 		tr.TransactionHash = fake.EthereumAddress()
	// 		tr.SendingAddress = fake.EthereumAddress()
	// 		tr.ReceivingAddress = fake.EthereumAddress()
	// 		tr.Protocol = "ETH"
	// 	} else {
	// 		tr.TransactionHash = fake.EOSTransactionHash()
	// 		tr.SendingAddress = fake.EOSAddress()
	// 		tr.ReceivingAddress = fake.EOSAddress()
	// 		tr.Protocol = "EOS"
	// 	}

	// 	tr.Timestamp = time.Now()

	// 	tr.Amount = rand.Float64() * 1000
	// 	tr.Fees = rand.Float64() * 10
	// 	tr.SendingName = usr.FirstName + " " + usr.LastName
	// 	tr.SendingUserId = usr.Id()
	// 	tr.SendingState = usr.KYC.Address.State
	// 	tr.SendingCountry = usr.KYC.Address.Country

	// 	tr.ReceivingName = usr2.FirstName + " " + usr2.LastName
	// 	tr.ReceivingUserId = usr2.Id()
	// 	tr.ReceivingState = usr2.KYC.Address.State
	// 	tr.SendingCountry = usr2.KYC.Address.Country
	// 	tr.MustPut()
	// }

	// for i := 0; i < 23; i++ {
	// 	d := disclosure.New(nsDb)
	// 	d.Publication = ""
	// 	d.Hash = fake.EOSTransactionHash()
	// 	d.Type = "prospectus"
	// 	d.MustPut()
	// }

	return org
})
