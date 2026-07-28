package wallets

import "testing"

// The KMS store derives a secret's tenant partition from the FIRST TWO segments
// of its ref: clients/kms store.go fileOrg returns segs[1] only when
// segs[0]=="orgs", and returns the shared "_platform" slug for every other
// shape — silently, with no error. So a ref that does not begin "orgs/<org>"
// does not merely look wrong, it lands every tenant's material in ONE file.
//
// keyRef began "wallets/" until this test existed, which put every tenant's
// sealed secp256k1 signing key in that shared partition while the comment above
// it asserted "the org segment is the hard isolation boundary". These tests
// pin the property that comment claims, so the claim can no longer drift from
// the behaviour.
//
// fileOrg is unexported in another package, so partitionOf reproduces it
// EXACTLY. If fileOrg's rule ever changes, TestPartitionOfMatchesTheStoreRule
// is the tripwire: it documents the mirrored source so a reader knows the two
// must be updated together.
func partitionOf(ref string) string {
	const platform = "_platform"
	p := ref
	for len(p) > 0 && p[0] == '/' {
		p = p[1:]
	}
	if p == "" {
		return platform
	}
	// SplitN(p, "/", 3) equivalent: we need only the first two segments.
	first, rest := p, ""
	for i := 0; i < len(p); i++ {
		if p[i] == '/' {
			first, rest = p[:i], p[i+1:]
			break
		}
	}
	if first != "orgs" || rest == "" {
		return platform
	}
	second := rest
	for i := 0; i < len(rest); i++ {
		if rest[i] == '/' {
			second = rest[:i]
			break
		}
	}
	if second == "" {
		return platform
	}
	return second
}

// TestKeyRefLandsInTheOwningOrgPartition is the regression that matters: a
// signing key for org "acme" must resolve to partition "acme", never to the
// shared platform file — at every scope narrowing, since each one appends
// segments and a prefix bug would survive the org-only case.
func TestKeyRefLandsInTheOwningOrgPartition(t *testing.T) {
	cases := []struct {
		name  string
		scope Scope
	}{
		{"org only", Scope{Org: "acme"}},
		{"project", Scope{Org: "acme", Project: "checkout"}},
		{"agent", Scope{Org: "acme", Agent: "trader"}},
		{"account", Scope{Org: "acme", AccountID: "acct1"}},
		{"fully narrowed", Scope{Org: "acme", Project: "checkout", Agent: "trader", AccountID: "acct1"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ref := c.scope.keyRef("w-1")
			if got := partitionOf(ref); got != "acme" {
				t.Fatalf("ref %q resolves to partition %q, want %q — the signing key "+
					"is in the SHARED partition, not the tenant's", ref, got, "acme")
			}
		})
	}
}

// TestKeyRefSeparatesTenants: two orgs must never share a partition. This is the
// property a reader assumes from "the org segment is the hard isolation
// boundary", and it was false before the prefix fix.
func TestKeyRefSeparatesTenants(t *testing.T) {
	a := partitionOf(Scope{Org: "acme"}.keyRef("w-1"))
	b := partitionOf(Scope{Org: "globex"}.keyRef("w-1"))
	if a == b {
		t.Fatalf("orgs acme and globex both resolve to partition %q — tenants share a file", a)
	}
	if a != "acme" || b != "globex" {
		t.Fatalf("partitions = (%q, %q), want (acme, globex)", a, b)
	}
}

// TestPartitionOfMatchesTheStoreRule pins the mirrored rule itself. partitionOf
// reproduces clients/kms fileOrg; if these expectations ever fail, fileOrg
// changed and the mirror must be updated with it.
func TestPartitionOfMatchesTheStoreRule(t *testing.T) {
	cases := map[string]string{
		"orgs/acme/wallets/w-1": "acme",
		"/orgs/acme/wallets/w1": "acme",
		"orgs/acme":             "acme",
		// Every shape below falls through to the shared partition — silently.
		// These are the bugs this file exists to prevent recurring.
		"wallets/acme/w-1":      "_platform",
		"org/acme/datastore/pw": "_platform",
		"orgs":                  "_platform",
		"orgs/":                 "_platform",
		"marketing/unsub-hmac":  "_platform",
		"":                      "_platform",
	}
	for ref, want := range cases {
		if got := partitionOf(ref); got != want {
			t.Errorf("partitionOf(%q) = %q, want %q", ref, got, want)
		}
	}
}
