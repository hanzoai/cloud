package wallet

import (
	"errors"

	"github.com/hanzoai/cloud/clients/commerce/models/blockchains"
)

// KeyGen returns (privateKeyHex, publicKeyHex, address) for a given chain
// type. Implementations register themselves via RegisterKeyGen. Bitcoin and
// Ethereum are wired in-tree (keygen_bitcoin.go / keygen_ethereum.go) because
// they only need luxfi/crypto primitives; the luxfi/cevm sub-module
// layers payment (JSON-RPC) support on top via RegisterPayment.
type KeyGen func(typ blockchains.Type) (priv, pub, address string, err error)

var keyGens = map[blockchains.Type]KeyGen{}

// RegisterKeyGen wires a key generator for a chain type. Later registrations
// override earlier ones; callers should register once at init time.
func RegisterKeyGen(typ blockchains.Type, fn KeyGen) {
	keyGens[typ] = fn
}

// ErrNoKeyGen is returned when CreateAccount is called for a chain type whose
// key-generator hasn't been registered.
var ErrNoKeyGen = errors.New("wallet: no key generator registered for chain type")

// generateKey dispatches to the registered generator for typ.
func generateKey(typ blockchains.Type) (priv, pub, address string, err error) {
	fn, ok := keyGens[typ]
	if !ok {
		return "", "", "", ErrNoKeyGen
	}
	return fn(typ)
}
