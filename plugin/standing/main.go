package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/standing"
)

// Standalone entry for the standing app.
//
// This is the app's OWN composition root: it links only its own subsystem and
// the cloud request tier, never the whole fleet, so the build is this one app
// and not the union of every other. The light host loads it as a plugin; run
// directly it serves standalone. Its OpenAPI subset comes from
// `standing openapi`.
//
// Free, because reporting what an entity costs to keep is a question a customer
// should never be charged to ask — it is the number that tells them whether to
// stay, and metering it would be charging for the truth about our own fee.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "standing",
		Price: cloud.Free,
		Use:   standing.Use,
	}}, []string{"standing"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
