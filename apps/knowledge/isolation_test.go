package knowledge

import "testing"

// isolation_test.go pins the per-org secret path of the KB lane: the KMS token
// path must be INJECTIVE in the org, so two distinct owners can never share a
// secret. (The passage store's boundary is the org's own file, named by
// orgns.OrgNamespace — passage_test.go proves no org reads another's.)

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
