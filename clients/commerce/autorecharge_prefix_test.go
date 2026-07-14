// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import "testing"

// TestAutoRechargePrefixMounted pins the wire path the durable cron's
// billing-autorecharge poke entry depends on: /v1/billing/auto-recharge must
// be a commerce prefix mount, or the poke's POST lands on the account-bridge
// /v1/billing/* catch-all whose session gate 403s the service token
// ("sign in to view billing") — which is exactly how the first live fires
// failed. The webhooks prefix is pinned alongside it: both are the
// token/signature-is-the-auth route families that must reach the commerce gin
// ahead of the bridge.
func TestAutoRechargePrefixMounted(t *testing.T) {
	want := map[string]bool{
		"/v1/billing/auto-recharge": false,
		"/v1/billing/webhooks":      false,
	}
	for _, p := range commercePrefixes {
		if _, ok := want[p]; ok {
			want[p] = true
		}
	}
	for p, ok := range want {
		if !ok {
			t.Errorf("commercePrefixes missing %q — the poke/webhook wire path 403s at the bridge", p)
		}
	}
}
