package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/account"
	"github.com/hanzoai/cloud/manifest"
)

// Standalone entry for the account app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `account openapi`. Hand-owned — edit the spec below directly.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "account",
		Price: cloud.Free,
		Mount: account.MountAccount,
		// DECLARED, because undeclared is not "no prefixes" — MountPrefixes falls
		// back to the /v1/<name> convention, and account answers at NONE of it:
		// its routes are /v1/keys, /v1/csrf, /v1/avatar, /v1/orgs, /v1/embed and
		// /v1/commerce/topup/*. The scope then installed this subsystem's
		// middleware on /v1/account, a path with no routes beneath it, and zip
		// refuses to compose a program whose middleware can never run — so the
		// plugin panicked at mount and every account route answered 503:
		//
		//   panic: the group "/v1/account" declares middleware at scope.go:134
		//          and no routes anywhere beneath it
		//
		// The same fallback bit analytics and entitlements before this.
		Prefixes: manifest.PrefixesFor("account"),
	}}, []string{"account"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
