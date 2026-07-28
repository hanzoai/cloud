package knowledge

import "testing"

// isolation_test.go pins the PHYSICAL per-org boundary of the KB lane: the Qdrant
// collection name and the KMS token path must be INJECTIVE in the org, so two
// distinct owners can never share a physical namespace or a secret path. This is the
// defense-in-depth layer under the payload.org filter (RED LOW-1): even a collection
// bug can't leak across tenants because the physical names are already disjoint.

// TestCollectionInjective_DistinctOrgs proves orgs that a naive space/underscore fold
// would collapse ("a b" vs "a_b") resolve to DISTINCT Qdrant collections.
func TestCollectionInjective_DistinctOrgs(t *testing.T) {
	x := &indexer{}
	pairs := [][2]string{
		{"a b", "a_b"},   // the classic space-vs-underscore collision
		{"a b", "a-b"},   // space-vs-dash
		{"ACME", "acme"}, // case fold would collapse these
		{"acme/evil", "acme-evil"},
	}
	for _, p := range pairs {
		c0, c1 := x.collection(p[0]), x.collection(p[1])
		if c0 == c1 {
			t.Errorf("collection collision: %q and %q both -> %q", p[0], p[1], c0)
		}
	}
}

// TestCollectionDeterministic proves the SAME org always yields the SAME collection
// (index and search must agree, or an org can't retrieve what it stored).
func TestCollectionDeterministic(t *testing.T) {
	x := &indexer{}
	for _, org := range []string{"acme", "a b", "ACME", "acme/evil"} {
		if x.collection(org) != x.collection(org) {
			t.Errorf("collection not deterministic for %q", org)
		}
	}
}

// TestKMSRefInjective_DistinctOrgs proves the per-org OAuth-token KMS path is
// injective in the org, so one org's sync can never read another org's token by a
// slug collision.
func TestKMSRefInjective_DistinctOrgs(t *testing.T) {
	pairs := [][2]string{
		{"a b", "a_b"},
		{"acme/evil", "acme-evil"},
		{"ACME", "acme"},
	}
	for _, p := range pairs {
		r0, r1 := kmsRef(p[0], "github"), kmsRef(p[1], "github")
		if r0 == r1 {
			t.Errorf("kmsRef collision: %q and %q both -> %q", p[0], p[1], r0)
		}
	}
}

// TestKMSRefSeparatesProviders proves one org's providers never share a path.
func TestKMSRefSeparatesProviders(t *testing.T) {
	if kmsRef("acme", "github") == kmsRef("acme", "slack") {
		t.Error("kmsRef must separate providers within an org")
	}
}
