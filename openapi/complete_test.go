package openapi

import (
	"testing"

	"github.com/hanzoai/cloud/manifest"
	"github.com/hanzoai/cloud/sdk/spec"
)

var routed = manifest.OwnerOf

// The rule is a RULE, not a patch for s3. [spec.Complete] attributes a
// declaration to the app the fleet routes its address to, and that holds only if
// the manifest routes each side of a shared product to its own app.
//
// Fourteen products are answered by more than one app, and every one of them is
// a pair the product segment collapses and longest prefix separates. For each,
// prose on the shallower app's address must not be charged to the deeper app.
// DERIVED from the manifest rather than listed, so the fifteenth pair is covered
// the day someone adds it — and so this test states the general fact rather than
// re-asserting the one instance that happened to break.
func TestTheManifestSeparatesEveryProductSharedByTwoApps(t *testing.T) {
	type claim struct{ app, prefix string }
	byProduct := map[string][]claim{}
	for _, a := range manifest.Apps {
		for _, p := range a.Prefixes {
			if pr := spec.Product(p); pr != "" {
				byProduct[pr] = append(byProduct[pr], claim{a.Name, p})
			}
		}
	}

	pairs := 0
	for product, claims := range byProduct {
		for _, mine := range claims {
			for _, sibling := range claims {
				if mine.app == sibling.app || mine.prefix == sibling.prefix {
					continue
				}
				pairs++
				t.Run(product+":"+sibling.app+"->"+mine.app, func(t *testing.T) {
					// The premise: one product, two apps, and the manifest routes
					// each prefix to its own. This is what Product() collapsed.
					if got := routed(sibling.prefix); got != sibling.app {
						t.Fatalf("%s routes to %q, not %q", sibling.prefix, got, sibling.app)
					}
					if got := routed(mine.prefix); got != mine.app {
						t.Fatalf("%s routes to %q, not %q", mine.prefix, got, mine.app)
					}
				})
			}
		}
	}
	if pairs == 0 {
		t.Fatal("no product is shared by two apps — this test has stopped testing anything; " +
			"if that is real, the collision class is gone and so is the reason for owner attribution")
	}
	t.Logf("%d shared-product app pairs, each routed to its own app", pairs)
}
