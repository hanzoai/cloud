package wallet

import (
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"strings"

	luxcrypto "github.com/luxfi/crypto"

	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
)

// Ethereum key generation lives here (not in luxfi/cevm) because the
// primitives rely only on luxfi/crypto (secp256k1 + keccak256 address
// derivation). The luxfi/cevm sub-module pulls the heavier luxfi/geth
// JSON-RPC client; it is not needed for generating accounts.
func init() {
	for _, typ := range []blockchains.Type{blockchains.EthereumType, blockchains.EthereumRopstenType} {
		RegisterKeyGen(typ, ethereumKeyGen)
	}
}

func ethereumKeyGen(_ blockchains.Type) (priv, pub, address string, err error) {
	sk, err := ecdsa.GenerateKey(luxcrypto.S256(), rand.Reader)
	if err != nil {
		return "", "", "", err
	}
	// Drop the leading 0x04 uncompressed-marker byte on the public key, matching
	// the format previously produced by luxfi/cevm.GenerateKeyPair.
	priv = hex.EncodeToString(luxcrypto.FromECDSA(sk))
	pub = hex.EncodeToString(luxcrypto.FromECDSAPub(&sk.PublicKey)[1:])
	address = strings.ToLower(luxcrypto.PubkeyToAddress(sk.PublicKey).Hex())
	return priv, pub, address, nil
}
