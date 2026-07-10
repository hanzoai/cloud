// Command husd-treasury-addr prints the EVM address of the HUSD treasury key
// (env HUSD_TREASURY_KEY). Address only — the key is never printed. Ops helper
// for funding the treasury on a fresh testnet. CGO-free.
package main

import (
	"fmt"
	"os"
	"strings"

	luxcrypto "github.com/luxfi/crypto"
)

func main() {
	key := strings.TrimPrefix(os.Getenv("HUSD_TREASURY_KEY"), "0x")
	if key == "" {
		fmt.Fprintln(os.Stderr, "set HUSD_TREASURY_KEY")
		os.Exit(1)
	}
	priv, err := luxcrypto.HexToECDSA(key)
	if err != nil {
		fmt.Fprintln(os.Stderr, "bad key:", err)
		os.Exit(1)
	}
	fmt.Println(strings.ToLower(luxcrypto.PubkeyToAddress(priv.PublicKey).Hex()))
}
