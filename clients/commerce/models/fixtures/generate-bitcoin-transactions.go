package fixtures

import (
	"errors"
	//"math/big"

	"github.com/gin-gonic/gin"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/datastore"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/wallet"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/bitcoin"
)

var GenerateTestBitcoinTransaction = New("generate-test-bitcoin-transaction", func(c *gin.Context) {
	db := datastore.New(c)
	ctx := db.Context

	w := wallet.New(db)
	w.Id_ = "test-btc-wallet"
	w.UseStringKey = true
	w.GetOrCreate("Id_=", "test-btc-wallet")

	if len(w.Accounts) == 0 {
		if _, err := w.CreateAccount("Test input account", blockchains.BitcoinTestnetType, []byte(config.Bitcoin.TestPassword)); err != nil {
			panic(err)
		}
		if _, err := w.CreateAccount("Test output account 1", blockchains.BitcoinTestnetType, []byte(config.Bitcoin.TestPassword)); err != nil {
			panic(err)
		}
		if _, err := w.CreateAccount("Test output account 2", blockchains.BitcoinTestnetType, []byte(config.Bitcoin.TestPassword)); err != nil {
			panic(err)
		}
	}

	sender, ok := w.GetAccountByName("Test input account")
	if !ok {
		panic(errors.New("Sender Account Not Found."))
	}
	receiver1, ok := w.GetAccountByName("Test output account 1")
	if !ok {
		panic(errors.New("Sender Account Not Found."))
	}
	receiver2, ok := w.GetAccountByName("Test output account 2")
	if !ok {
		panic(errors.New("Sender Account Not Found."))
	}

	log.Info("Accounts Found", ctx)
	log.Info("Sender Address", sender.Address)
	log.Info("Receiver 1 Address", receiver1.Address)
	log.Info("Receiver 2 Address", receiver2.Address)
	if err := sender.Decrypt([]byte(config.Bitcoin.TestPassword)); err != nil {
		panic(err)
	}
	if err := receiver1.Decrypt([]byte(config.Bitcoin.TestPassword)); err != nil {
		panic(err)
	}
	if err := receiver2.Decrypt([]byte(config.Bitcoin.TestPassword)); err != nil {
		panic(err)
	}

	in := []bitcoin.Origin{bitcoin.Origin{TxId: "5b60d0684a8201ddac20f713782a1f03682b508e90d99d0887b4114ad4ccfd2c", OutputIndex: 0}}
	out := []bitcoin.Destination{bitcoin.Destination{Value: 1000, Address: receiver1.Address}, bitcoin.Destination{Value: 5000, Address: receiver2.Address}}
	senderAccount := bitcoin.Sender{
		PrivateKey: sender.PrivateKey,
		PublicKey:  sender.PublicKey,
		Address:    sender.Address,
	}

	client := bitcoin.New(db.Context, config.Bitcoin.TestNetNodes[0], config.Bitcoin.TestNetUsernames[0], config.Bitcoin.TestNetPasswords[0])

	log.Info("Created Bitcoin client.")

	rawTrx, _ := bitcoin.CreateTransaction(client, in, out, senderAccount, 0)

	log.Info("Raw transaction hex: %v", rawTrx)

	log.Info("Not sending raw transaction because this is a generation Fixture. Check the Send-bitcoin-transaction to actually send it to the node.")
})
