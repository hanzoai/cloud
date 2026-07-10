package ethereum

import (
	"context"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/wallet"
	blockchainutil "github.com/hanzoai/cloud/clients/commerce/util/blockchain"
)

// init wires EVM payment support into the parent commerce module. Key
// generation lives in-tree (models/wallet/keygen_ethereum.go) because it only
// needs luxfi/crypto; only the JSON-RPC payment client requires luxfi/geth,
// so the split is: parent does ETH accounts, sub-module does ETH payments.
func init() {
	for _, typ := range []blockchains.Type{blockchains.EthereumType, blockchains.EthereumRopstenType} {
		blockchainutil.RegisterPayment(typ, ethereumPayment)
	}
}

func ethereumPayment(ctx context.Context, from wallet.Account, to string, amount, fee currency.Cents, password []byte) (string, error) {
	client := Client{}
	switch from.Type {
	case blockchains.EthereumType:
		client = New(ctx, config.Ethereum.MainNetNodes[0])
		client.Chain = MainNet
	case blockchains.EthereumRopstenType:
		client = New(ctx, config.Ethereum.TestNetNodes[0])
		client.Chain = Ropsten
	}

	if from.PrivateKey == "" {
		if err := from.Decrypt(password); err != nil {
			return "", err
		}
	}

	amt := currency.ETH.ToMinimalUnits(amount)
	gasPrice := currency.ETH.ToMinimalUnits(fee)
	return MakePayment(client, from.PrivateKey, from.Address, to, amt, gasPrice, client.Chain)
}
