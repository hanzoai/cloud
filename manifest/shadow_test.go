// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package manifest

import "testing"

// TestNoShadowedPrefix pins the one invariant the manifest cannot express in its
// own syntax: a prefix belongs to exactly ONE app.
//
// The router registers All(prefix) + All(prefix+"/*") per app and MERGES
// identical patterns, appending the later handler behind the earlier one. The
// earlier handler is a proxy that never calls Next(), so a duplicated prefix
// does not conflict loudly — the second app simply never runs. Two collisions
// shipped exactly that way: provisioning's bare "/v1/s3" sat behind storage's
// while both rows read "/v1/s3" (storage now claims the deeper /v1/s3/buckets
// and /v1/s3/health instead), and zen's "/v1" Claim() metering sat inert behind
// the serving owner until Coresident+Gates let a middleware mount stop claiming
// a route. Both fixes changed the table; this keeps a new pair from shipping.
//
// NESTED prefixes stay legal — provisioning /v1/vector beside product
// /v1/vector/collections is longest-match routing (OwnerOf), not shadowing.
// Gates are exempt by construction: a gate wraps a subtree, it does not claim
// to serve it, which is exactly what separates the two fields.
func TestNoShadowedPrefix(t *testing.T) {
	owner := map[string]string{}
	for _, a := range Apps {
		for _, p := range a.Prefixes {
			prev, dup := owner[p]
			if !dup {
				owner[p] = a.Name
				continue
			}
			t.Errorf("prefix %q is claimed by both %q and %q — %q wins and %q is unreachable",
				p, prev, a.Name, prev, a.Name)
		}
	}
}
