package blockchain

import (
	"context"
	"errors"
	"fmt"

	"github.com/hanzoai/cloud/clients/commerce/config"
	"github.com/hanzoai/cloud/clients/commerce/log"
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/models/types/currency"
	"github.com/hanzoai/cloud/clients/commerce/models/wallet"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/bitcoin"
	"github.com/hanzoai/cloud/clients/commerce/util/json"
)

func MakeBitcoinPayment(ctx context.Context, from wallet.Account, to string, amount, feePerByte currency.Cents, password []byte) (string, error) {
	// Create needed client.

	client := bitcoin.BitcoinClient{}
	switch from.Type {
	case blockchains.BitcoinType:
		client = bitcoin.New(ctx, config.Bitcoin.MainNetNodes[0], config.Bitcoin.MainNetUsernames[0], config.Bitcoin.MainNetPasswords[0])
	case blockchains.BitcoinTestnetType:
		client = bitcoin.New(ctx, config.Bitcoin.TestNetNodes[0], config.Bitcoin.TestNetUsernames[0], config.Bitcoin.TestNetPasswords[0])
	default:
		return "", errors.New(fmt.Sprintf("Unsupported blockchain type: %v", from.Type))
	}

	oris, err := bitcoin.GetBitcoinTransactions(ctx, from.Address)
	if err != nil {
		log.Info("Address '%s' Transaction: %v", from.Address, json.Encode(oris), ctx)
		return "", err
	}

	total := int64(amount)

	prunedOris, err := bitcoin.PruneOriginsWithAmount(oris, total)
	if err != nil {
		log.Info("Address '%s' Transaction: %v", from.Address, json.Encode(prunedOris), ctx)
		return "", err
	}

	in := bitcoin.OriginsWithAmountToOrigins(prunedOris)
	out := []bitcoin.Destination{
		bitcoin.Destination{
			Value:   total,
			Address: to,
		},
	}

	// Decrypt private key if needed.
	if from.Encrypted != "" && from.Salt != "" && from.PrivateKey == "" {
		err = from.Decrypt(password)
	}

	rawTrx, err := bitcoin.CreateTransaction(client, in, out, bitcoin.Sender{
		PrivateKey: from.PrivateKey,
		PublicKey:  from.PublicKey,
		Address:    from.Address,
	}, int64(feePerByte))

	return client.SendRawTransaction(rawTrx)
}
