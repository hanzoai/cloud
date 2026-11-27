package wallet

import (
	"testing"

	"github.com/hanzoai/cloud/apps/kms"
)

// A wallet's signing key is sealed on its org's behalf, so the store answers it
// to this app and to no member over REST (apps/kms custody.go). Every scope's
// ref has to land in that custody, narrowed or not.
func TestEveryWalletKeyIsCustodied(t *testing.T) {
	for _, s := range []Scope{
		{Org: "acme"},
		{Org: "acme", Project: "checkout"},
		{Org: "acme", Agent: "trader"},
		{Org: "acme", AccountID: "acct1"},
		{Org: "acme", Project: "checkout", Agent: "trader", AccountID: "acct1"},
	} {
		if ref := s.keyRef("w1"); !kms.Custodied(ref) {
			t.Errorf("%s is a wallet's signing key and is not custodied", ref)
		}
	}
}
