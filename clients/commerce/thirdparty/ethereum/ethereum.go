package ethereum

import (
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"

	luxcrypto "github.com/luxfi/crypto"
	"github.com/hanzoai/cloud/clients/commerce/log"
)

type ChainId int64

const (
	MainNet ChainId = 1
	Morden  ChainId = 2
	Ropsten ChainId = 3
)

const (
	DefaultGas      int64 = 90000
	DefaultGasPrice int64 = 1 * Shannon
)

func GenerateKeyPair() (string, string, string, error) {
	priv, err := ecdsa.GenerateKey(luxcrypto.S256(), rand.Reader)
	if err != nil {
		return "", "", "", err
	}

	// Remove the extra pubkey byte before serializing hex (drop the first 0x04)
	return hex.EncodeToString(luxcrypto.FromECDSA(priv)), hex.EncodeToString(luxcrypto.FromECDSAPub(&priv.PublicKey)[1:]), PubkeyToAddress(priv.PublicKey), nil
}

func PubkeyToAddress(p ecdsa.PublicKey) string {
	// Remove the '0x' from the address
	return luxcrypto.PubkeyToAddress(p).Hex()
}

func MakePayment(client Client, pk string, from string, to string, amount, gasPrice *big.Int, chainId ChainId) (string, error) {
	balance, err := client.GetBalance(from)
	if err != nil {
		return "", err
	}
	if balance.Cmp(amount) != 1 && !IsTest {
		err = errors.New(fmt.Sprintf("Insufficient funds for address %v. Requested to send %v, only %v available.", from, amount, balance))
		log.Error(err)
		return "", err
	}
	transactionId, err := client.SendTransaction(chainId, pk, from, to, amount, big.NewInt(0), gasPrice, nil)
	return transactionId, err
}
