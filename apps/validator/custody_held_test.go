package validator

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/apps/kms"
)

// A validator's staking material is sealed beneath its slot's folder,
// orgs/<org>/validators/<tokenId>/<artifact>, which the store keeps on the org's
// behalf and opens over REST to the org's KMS-sync identity alone (apps/kms
// custody.go). The folder is at the subtree's root, and a key sealed there is not
// custodied — so every artifact has to land beneath it, and the custody sweep
// names this test as what holds kmsStakingBaseRef to that.
func TestEveryStakingArtifactIsCustodied(t *testing.T) {
	id, err := generateStakingIdentity()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	sealed := &fakeKMS{}
	if err := id.seal(context.Background(), sealed, kmsStakingBaseRef("acme", 7)); err != nil {
		t.Fatalf("seal: %v", err)
	}
	if len(sealed.store) != len(stakingArtifacts) {
		t.Fatalf("sealed %d refs, want one per artifact (%d)", len(sealed.store), len(stakingArtifacts))
	}
	for ref := range sealed.store {
		if !kms.Custodied(ref) {
			t.Errorf("%s is a validator's staking material and is not custodied", ref)
		}
	}
	if kms.Custodied(kmsStakingBaseRef("acme", 7)) {
		t.Fatal("the slot's folder itself reads as custodied; the sweep's premise is wrong")
	}
}
