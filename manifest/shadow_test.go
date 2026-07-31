package manifest

import "testing"

// TestNoShadowedPrefix pins the one invariant the manifest cannot express in its
// own syntax: a prefix belongs to exactly ONE app.
//
// The router registers All(prefix) + All(prefix+"/*") per app and MERGES
// identical patterns, appending the later handler behind the earlier one. The
// earlier handler is a proxy that never calls Next(), so a duplicated prefix
// does not conflict loudly — the second app simply never runs. Two shipped
// examples: provisioning's "/v1/s3" sat behind storage's, which cost it four
// real routes (its s3-kind CRUD, now at /v1/object), and zen's "/v1" sits behind
// commerce's, which silently disables zen's Claim() metering middleware.
//
// manifest_test.go asserts only that mounting does not panic, which is why both
// shipped. This asserts ownership.
// knownShadowed records collisions that are REAL BUGS not yet fixed, so this
// gate blocks new ones without hiding the old. It must only ever shrink.
//
// "/v1": zen mounts Group("/v1", z.Claim()) — spend/claim MIDDLEWARE, not
// routes (apps/zen/zen.go:84) — and commerce's terminal "/v1" proxy runs first
// and never calls Next(), so the claim gate is inert. Fixing it turns metering
// on for the inference path, which is a billing decision, not a refactor: it
// needs an owner's sign-off and a metered-vs-unmetered reconciliation, so it is
// recorded here rather than flipped silently. The durable fix is to let the
// manifest distinguish a middleware mount from a route owner, so ordering
// stops being load-bearing.
var knownShadowed = map[string]string{
	"/v1": "zen Claim() metering middleware inert behind commerce — needs billing sign-off",
}

func TestNoShadowedPrefix(t *testing.T) {
	owner := map[string]string{}
	seen := map[string]bool{}
	for _, a := range Apps {
		for _, p := range a.Prefixes {
			prev, dup := owner[p]
			if !dup {
				owner[p] = a.Name
				continue
			}
			if why, known := knownShadowed[p]; known {
				seen[p] = true
				t.Logf("known shadowed prefix %q (%s vs %s): %s", p, prev, a.Name, why)
				continue
			}
			t.Errorf("prefix %q is claimed by both %q and %q — %q wins and %q is unreachable",
				p, prev, a.Name, prev, a.Name)
		}
	}
	// A fixed collision must be struck from the list, or the list rots into a
	// permanent excuse.
	for p := range knownShadowed {
		if !seen[p] {
			t.Errorf("knownShadowed lists %q but it no longer collides — delete the entry", p)
		}
	}
}
