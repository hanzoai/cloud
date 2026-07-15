// Copyright © 2026 Hanzo AI. MIT License.

package subsystems

import "testing"

// TestCommercePrefixesPinned pins the wire paths that must reach the commerce
// gin handler ahead of the account-bridge /v1/billing/* catch-all — the
// token/signature-is-the-auth route families a session gate would break:
//
//   - /v1/billing/webhooks       provider HMAC is the auth (Square et al)
//   - /v1/billing/auto-recharge  the durable cron's billing-autorecharge poke
//     (COMMERCE_SERVICE_TOKEN bearer); without the prefix the poke 403s at the
//     bridge ("sign in to view billing") — exactly how the first live fires
//     failed, and how the commerce unfork regressed it once already by
//     snapshotting a pre-fix prefix list.
func TestCommercePrefixesPinned(t *testing.T) {
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
