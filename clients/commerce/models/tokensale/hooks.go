package tokensale

import (
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/util/rand"
)

// Hooks
func (ts *TokenSale) BeforeCreate() error {
	ts.WalletPassphrase = rand.SecretKey()
	w, err := ts.GetOrCreateWallet(ts.Datastore())
	if err != nil {
		return err
	}

	_, err = w.CreateAccount("default", blockchains.EthereumType, []byte(ts.WalletPassphrase))
	return err
}
