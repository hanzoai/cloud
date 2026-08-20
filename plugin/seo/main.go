package main

import (
	"fmt"
	"os"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/seo"
)

// Standalone entry for the seo app.
//
// This is the app's OWN composition root — it links only apps/seo and the
// cloud request tier, never package apps, so the build is this one subsystem
// and not the whole fleet. The light host loads it as a plugin; run directly it
// serves standalone. Its OpenAPI subset comes from `seo openapi`.
//
// Metered, not a flat edge price. Every call here buys a measurement from an
// upstream that charges per call and, for the list ops, per row — so what one
// request costs is a property of what it asked for, which only the app knows. The
// edge therefore charges nothing and apps/seo/typed.go owns the debit: it
// authorizes the caller against the vendor's published quote before the call and
// debits the vendor's own stated charge after it. A flat price at the door would
// bill a hundred-row expansion and a single backlink summary the same, and would
// bill it a second time on top of the meter.
func main() {
	if err := cloud.Listen([]cloud.Plugin{{
		Name:  "seo",
		Price: cloud.Metered,
		Mount: seo.Mount,
	}}, []string{"seo"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
