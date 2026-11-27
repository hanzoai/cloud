// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"testing"

	commercebilling "github.com/hanzoai/commerce/api/billing"

	"github.com/hanzoai/cloud/client"
)

// Whose subscriptions a cancel or a resume may change is the endpoint's to decide,
// and it arrives as a value: the org's own authority holds every row, a wallet
// holds its own, and a ref that states neither holds nothing — so a caller the
// endpoint did not vouch for reaches no row at all.
func TestARefHoldsWhatTheEndpointGranted(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ref   client.SubscriptionRef
		owner string
		holds bool
	}{
		{"the org's authority, any wallet", client.SubscriptionRef{Privileged: true, Subject: "acme"}, "acme/carol", true},
		{"the org's authority, the org", client.SubscriptionRef{Privileged: true}, "acme", true},
		{"a wallet, its own row", client.SubscriptionRef{Subject: "hanzo/alice"}, "hanzo/alice", true},
		{"a wallet, a neighbour's row", client.SubscriptionRef{Subject: "hanzo/alice"}, "hanzo/bob", false},
		{"a wallet, the org's row", client.SubscriptionRef{Subject: "hanzo/alice"}, "hanzo", false},
		{"nothing stated", client.SubscriptionRef{}, "acme", false},
		{"nothing stated, an empty owner", client.SubscriptionRef{}, "", false},
	} {
		if got := commercebilling.Holds(holderOf(&tc.ref), tc.owner); got != tc.holds {
			t.Errorf("%s: holds %q = %v, want %v", tc.name, tc.owner, got, tc.holds)
		}
	}
}
