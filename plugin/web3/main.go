package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/web3"
)

// Standalone entry for the web3 app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the union the fused binary was. The light host loads it as a plugin;
// run directly it serves standalone.
//
// It replaces hanzoai/bootnode's api/ — 102 Python files of 2018-era Quart that,
// by the end, answered four routes. The chain surface now lives where every
// other Hanzo subsystem lives, in the one Go binary, and is reached at
// api.hanzo.ai/v1 like everything else rather than at its own host.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:     "web3",
		Price:    cloud.Free,
		Use:      web3.Use,
		Shutdown: cloud.CtxShutdown(web3.Shutdown),
	}}, []string{"web3"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
