// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package billing

import (
	"net/http"
	"testing"
)

// TIER IS THE SIBLING BALANCE ALREADY FIXED, and it did not follow.
//
// balance() resolves its tenant with readerOrg, which admits a validated session
// OR a trusted service naming its org — because every app is its own child
// PROCESS, a Go func var cannot cross that boundary, and ai therefore reads this
// over HTTP carrying COMMERCE_SERVICE_TOKEN rather than a session. The typed ops
// resolved through payer, which asked principal.OrgFrom alone, so the same caller
// balance admits was refused here:
//
//	GET /v1/billing/tier?user=hanzo  Bearer $COMMERCE_SERVICE_TOKEN  X-Org-Id: hanzo
//	  → 401 {"detail":"sign in to view finance"}
//
// Measured against the deployed commerce with a token the SAME request proves
// good: /v1/billing/balance answered 200 and /v1/billing/tier answered 401, on
// one host, one token, two sibling routes.
//
// The cost is quiet rather than loud. The rate limiter maps any failed tier read
// to the lowest tier, so every paying org was shaped as free; the per-SKU gate
// maps one to "unknown" and ALLOWS, so it stopped gating. One refusal, two
// subsystems, opposite directions, neither of them an error anybody saw.
//
// This asserts admission, not the tier: the plane has no commerce behind it here,
// so the read may still fail — but it must fail as something other than "who are
// you", which is the only thing payer decides.
func TestTier_TrustedS2SReadIsServed(t *testing.T) {
	const token = "test-commerce-service-token"
	app := mountApp(t, "", token)

	code, body := s2sCall(t, app, "/v1/billing/tier?user=hanzo", token, "hanzo")
	if code == http.StatusUnauthorized {
		t.Fatalf("the trusted S2S caller was refused as unauthenticated: %s", body)
	}
}

// The other half, and the reason this is not a widening: admitting a service
// token must not become a way to read another tenant's plan, or to skip auth.
// The org rides the gateway-pinned X-Org-Id — a header the gateway STRIPS from
// every client request — never a caller-supplied field. A wrong token, an absent
// token, or a token naming no org must all still refuse, exactly as they do on
// balance.
func TestTier_S2SDoesNotWidenScope(t *testing.T) {
	const token = "test-commerce-service-token"

	for _, tc := range []struct {
		name  string
		token string
		org   string
	}{
		{"wrong token with an org", "not-the-token", "hanzo"},
		{"empty token with an org", "", "hanzo"},
		{"valid token but no org to scope to", token, ""},
		{"near-miss token (prefix)", "test-commerce-service-toke", "hanzo"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := mountApp(t, "", token)

			code, _ := s2sCall(t, app, "/v1/billing/tier?user=hanzo", tc.token, tc.org)
			if code != http.StatusUnauthorized {
				t.Errorf("want 401, got %d", code)
			}
		})
	}
}
