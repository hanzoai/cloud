package wallet

import (
	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
	"github.com/hanzoai/cloud/clients/commerce/thirdparty/bitcoin"
)

func init() {
	for _, typ := range []blockchains.Type{blockchains.BitcoinType, blockchains.BitcoinTestnetType} {
		RegisterKeyGen(typ, bitcoinKeyGen)
	}
}

func bitcoinKeyGen(typ blockchains.Type) (priv, pub, address string, err error) {
	priv, pub, err = bitcoin.GenerateKeyPair()
	if err != nil {
		return "", "", "", err
	}
	switch typ {
	case blockchains.BitcoinType:
		address, _, err = bitcoin.PubKeyToAddress(pub, false)
	case blockchains.BitcoinTestnetType:
		address, _, err = bitcoin.PubKeyToAddress(pub, true)
	default:
		err = ErrorInvalidTypeSpecified
	}
	if err != nil {
		return "", "", "", err
	}
	return priv, pub, address, nil
}
