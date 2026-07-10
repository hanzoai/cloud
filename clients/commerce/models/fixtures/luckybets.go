package fixtures

import (
	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/auth/password"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	"github.com/hanzoai/cloud/clients/commerce/types/email"
	"github.com/hanzoai/cloud/clients/commerce/types/website"
)

var _ = New("luckybets", func(c *gin.Context) *organization.Organization {
	db := datastore.New(c)

	org := organization.New(db)
	org.Name = "luckybets"
	org.GetOrCreate("Name=", org.Name)

	u := user.New(db)
	u.Email = "zach@hanzo.ai"
	u.GetOrCreate("Email=", u.Email)
	u.FirstName = "Zach"
	u.LastName = "Kelling"
	u.Organizations = []string{org.Id()}
	u.PasswordHash, _ = password.Hash("Xtr3Lk7R")
	u.Put()

	org.FullName = "Lucky Bets"
	org.Owners = []string{u.Id()}
	org.Websites = []website.Website{website.Website{Type: website.Production, Url: "http://www.luckybets.co"}}
	org.SecretKey = []byte("iBuGZ6krwUvMItvTX7Rl6OevF23Yl40T")

	org.Fees.Card.Flat = 0
	org.Fees.Card.Percent = 0
	org.Fees.Affiliate.Flat = 0
	org.Fees.Affiliate.Percent = 0
	org.Fees.Ethereum.Flat = 0
	org.Fees.Ethereum.Percent = 0.0
	org.Fees.Bitcoin.Flat = 0
	org.Fees.Bitcoin.Percent = 0.0

	// Email configuration
	org.Mandrill.APIKey = "wJ3LGLp5ZOUZlSH8wwqmTg"

	org.Email.Enabled = true
	org.Email.Defaults.From = email.Email{
		Name:    "Admin",
		Address: "noreply@hanzo.ai",
	}

	org.Email.Order.Confirmation.Subject = "Deposit confirmation"
	org.Email.Order.Confirmation.Enabled = true

	/*org.Email.User.PasswordReset.Subject = "Reset your password"
	org.Email.User.PasswordReset.Enabled = true

	// org.Email.User.EmailConfirmation.Subject = ""
	org.Email.User.EmailConfirmation.Enabled = false

	org.Email.User.EmailConfirmed.Subject = "Complete registration"
	org.Email.User.EmailConfirmed.Enabled = true*/

	// Save org into default namespace
	org.MustUpdate()

	org.WalletPassphrase = "wsnwN6aBysgUGD55WugaJzpMFJRrqFfcxnWPELEsd7aP7abQNK7byMebf5nD9JJpgGytykBamThQVKpXuBKRKVRWU3GTUAHAmvAq8gFypJ2aAbVcU569NYbFRpR7b8zH"

	wal, err := org.GetOrCreateWallet(org.Datastore())
	if err != nil {
		panic(err)
	}

	_, err = wal.CreateAccount("ethereum", blockchains.EthereumType, []byte(org.WalletPassphrase))
	if err != nil {
		panic(err)
	}
	_, err = wal.CreateAccount("ethereum-ropsten", blockchains.EthereumRopstenType, []byte(org.WalletPassphrase))
	if err != nil {
		panic(err)
	}
	_, err = wal.CreateAccount("bitcoin", blockchains.BitcoinType, []byte(org.WalletPassphrase))
	if err != nil {
		panic(err)
	}
	_, err = wal.CreateAccount("bitcoin-testnet", blockchains.BitcoinTestnetType, []byte(org.WalletPassphrase))
	if err != nil {
		panic(err)
	}

	// if a, _ := w.GetAccountByName("cryptounderground-test"); a == nil {
	// 	if _, err := w.CreateAccount("cryptounderground-test", blockchains.EthereumRopstenType, []byte("7MdTrG3jzZD2h6T9src25r5aaC29MCyZ")); err != nil {
	// 		panic(err)
	// 	}
	// }

	// if a, _ := w.GetAccountByName("cryptounderground"); a == nil {
	// 	if _, err := w.CreateAccount("cryptounderground", blockchains.EthereumType, []byte("7MdTrG3jzZD2h6T9src25r5aaC29MCyZ")); err != nil {
	// 		panic(err)
	// 	}
	// }

	return org
})
