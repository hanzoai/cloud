package fixtures

import (
	//"math/big"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/bitcoin"
)

var GetTestBitcoinTransaction = New("test-bitcoin-gettransaction", func(c *gin.Context) {
	db := datastore.New(c)

	client := bitcoin.New(db.Context, config.Bitcoin.TestNetNodes[0], config.Bitcoin.TestNetUsernames[0], config.Bitcoin.TestNetPasswords[0])
	log.Info("Created Bitcoin client.")

	res, err := client.GetRawTransaction("5b60d0684a8201ddac20f713782a1f03682b508e90d99d0887b4114ad4ccfd2c")
	if err != nil {
		panic(err)
	}

	log.Info("Btcd Node Response: %v", "", res)
})
