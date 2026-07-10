package ethereum

import (
	"errors"
	// "math/big"

	// "github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/order"
	"github.com/hanzoai/cloud/clients/commerce/models/organization"
	"github.com/hanzoai/cloud/clients/commerce/models/user"
	// "github.com/hanzoai/cloud/clients/commerce/models/wallet"
	// "github.com/hanzoai/cloud/clients/commerce/luxfi/cevm"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/util/rand"
)

var PlatformWalletNotFound = errors.New("Platform Wallet Not Found.")
var PlatformAccountNotFound = errors.New("Platform Account Not Found.")
var PlatformAccountDecryptionFailed = errors.New("Platform Account Decryption Failed.")

// This creates the wallet for
func Authorize(org *organization.Organization, ord *order.Order, usr *user.User) error {
	ctx := org.Datastore().Context

	w, err := ord.GetOrCreateWallet(ord.Datastore())
	if err != nil {
		return err
	}

	ord.WalletPassphrase = rand.SecretKey()

	if ord.Test {
		// 	pw := wallet.New(org.Datastore())
		// 	if ok, err := pw.Query().Filter("Id_=", "platform-wallet").Get(); !ok {
		// 		if err != nil {
		// 			return err
		// 		}
		// 		return PlatformWalletNotFound
		// 	}

		// 	// Find The Test Account
		// 	account, ok := pw.GetAccountByName("Ethereum Ropsten Test Account")
		// 	if !ok {
		// 		return PlatformAccountNotFound
		// 	}

		// 	log.Info("Account Found", ctx)
		// 	if err := account.Decrypt([]byte(config.Ethereum.TestPassword)); err != nil {
		// 		return err
		// 	}

		// 	if account.PrivateKey == "" {
		// 		return PlatformAccountDecryptionFailed
		// 	}

		log.Info("Ethereum Test Mode", ctx)
		if _, err = w.CreateAccount("Receiver Account", blockchains.EthereumRopstenType, []byte(ord.WalletPassphrase)); err != nil {
			return err
		}

		// 	client := ethereum.New(org.Datastore().Context, config.Ethereum.TestNetNodes[0])

		// 	if _, err := client.SendTransaction(
		// 		ethereum.Ropsten,
		// 		account.PrivateKey,
		// 		account.Address,
		// 		w.Accounts[0].Address,
		// 		ord.Currency.ToMinimalUnits(ord.Total),
		// 		big.NewInt(0),
		// 		big.NewInt(0),
		// 		[]byte{},
		// 	); err != nil {
		// 		return err
		// 	}
	} else {
		log.Info("Ethereum Production Mode", ctx)
		if _, err = w.CreateAccount("Receiver Account", blockchains.EthereumType, []byte(ord.WalletPassphrase)); err != nil {
			return err
		}
	}
	return err
}
