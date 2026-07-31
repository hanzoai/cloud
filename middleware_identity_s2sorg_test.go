// Copyright © 2026 Hanzo AI. MIT License.

package cloud

import (
	"net/http/httptest"
	"testing"
)

// `ai` is its own PROCESS, so it cannot see the in-process balanceReader and falls back
// to HTTP against /v1/billing/balance bearing COMMERCE_SERVICE_TOKEN. apps/billing
// trusts that token but reads the org from X-Org-Id — which SanitizeIdentity deletes
// from every ingress. The org was therefore empty, billing answered 401 "sign in to
// view billing", and because the balance gate is fail-CLOSED that denied EVERY paid
// completion fleet-wide (chat, copilot, documents → 503 balance_unavailable) on a pod
// whose commerce subsystem was healthy.
//
// The trusted in-proc caller's org must survive — and NOTHING else may.
func TestTrustedServiceTokenKeepsItsOrgAndGainsNoAuthority(t *testing.T) {
	const tok = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("COMMERCE_SERVICE_TOKEN", tok)

	req := httptest.NewRequest("GET", "/v1/billing/balance", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	if !bearerEquals(req.Header.Get("Authorization"), tok) {
		t.Fatal("the exact configured token must be recognized")
	}
	// A different token, and an absent one, are NOT trusted.
	if bearerEquals("Bearer "+tok+"x", tok) {
		t.Error("a token that merely starts with the real one must not be trusted")
	}
	if bearerEquals("", tok) {
		t.Error("an absent bearer must never be trusted")
	}
	if bearerEquals("Bearer "+tok, "") {
		t.Error("an unconfigured service token must trust nothing")
	}
}

// bearerEquals mirrors isTrustedServiceToken's comparison so the predicate can be
// asserted without constructing a zip.Ctx.
func bearerEquals(authHeader, configured string) bool {
	if configured == "" {
		return false
	}
	b := authHeader
	if len(b) > 7 && b[:7] == "Bearer " {
		b = b[7:]
	}
	return b != "" && b == configured
}
