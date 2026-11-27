package provisioning

import (
	"testing"

	"github.com/hanzoai/cloud/apps/kms"
)

// A provisioned resource's admin password is sealed at orgs/<org>/<kind>/<name>
// on the org's behalf, so every kind is a subtree the store keeps from the org's
// members over REST (apps/kms custody.go). A new kind fails here until it is.
func TestEveryKindsPasswordIsCustodied(t *testing.T) {
	for _, kind := range kinds {
		if ref := "orgs/acme/" + kind + "/main"; !kms.Custodied(ref) {
			t.Errorf("%s is a %s resource's admin password and is not custodied", ref, kind)
		}
	}
}
