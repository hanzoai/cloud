package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/wallet"
)

// Standalone entry for the wallets app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `wallets openapi`. Hand-owned — edit the spec below directly.
//
// Metered, not Free: creating a wallet under MPC, treasury or Safe custody runs a
// distributed CGGMP21 keygen on the ring, and a Safe additionally deploys a
// contract with real gas; signing and proposing each run a threshold round. The
// surface is org-scoped with no admin gate, so a tenant reaches all of it. The
// meter is apps/wallet/meter.go, and custody is the predicate — KindKMS is an
// in-process keygen, buys nothing, and stays free.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "wallet",
		Price:    cloud.Metered,
		Use:      wallet.Use,
		Shutdown: cloud.CtxShutdown(wallet.Shutdown),
	}}, []string{"wallet"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
