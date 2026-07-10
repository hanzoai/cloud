package fixtures

import (
	"errors"
	"math/big"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	commercefixtures "github.com/hanzoai/cloud/clients/commerce/models/fixtures"
	"github.com/hanzoai/cloud/clients/commerce/models/wallet"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/ethereum"
)

var SendTestEthereumTransaction = commercefixtures.New("send-test-ethereum-transaction", func(c *gin.Context) {
	db := datastore.New(c)
	ctx := db.Context

	w := wallet.New(db)
	w.Id_ = "test-customer-wallet"
	w.UseStringKey = true
	w.GetOrCreate("Id_=", "test-customer-wallet")

	if len(w.Accounts) == 0 {
		if _, err := w.CreateAccount("Test Customer Account", blockchains.EthereumRopstenType, []byte(config.Ethereum.TestPassword)); err != nil {
			panic(err)
		}
	}

	pw := wallet.New(db)
	pw.GetOrCreate("Id_=", "platform-wallet")

	// Find The Test Account
	account, ok := pw.GetAccountByName("Ethereum Ropsten Test Account")
	if !ok {
		panic(errors.New("Platform Account Not Found."))
	}

	log.Info("Account Found", ctx)
	if err := account.Decrypt([]byte(config.Ethereum.TestPassword)); err != nil {
		panic(err)
	}

	client := ethereum.New(db.Context, config.Ethereum.TestNetNodes[0])

	hash, err := client.SendTransaction(ethereum.Ropsten, account.PrivateKey, account.Address, w.Accounts[0].Address, big.NewInt(1000000000000000), big.NewInt(0), big.NewInt(0), []byte{})
	if err != nil {
		panic(err)
	}

	log.Info("Geth Node Response: %v", hash, c)
})
