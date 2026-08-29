package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/share"
)

// Standalone entry for the share app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the ~3040-package union the fused binary was. The light host loads it
// as a plugin; run directly it serves standalone. Its OpenAPI subset comes from
// `share openapi`. Hand-owned — edit the spec below directly.
// Metered, not Free: POST /v1/share/enable mints an account on the share fabric
// using the PLATFORM's admin credential and hands the caller the token their CLI
// enables tunnels with. It is org-scoped with no admin gate — self-service is the
// product — so any tenant reaches it. The meter is apps/share/meter.go, and it
// charges the PROVISION only: reading back an account you already have is free,
// because enable is idempotent and a caller re-reading their own token has bought
// nothing.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "share",
		Price: cloud.Metered,
		Use:   share.Use,
	}}, []string{"share"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
