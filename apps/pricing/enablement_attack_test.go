package pricing

// RED attack tests — the enablement self-opt-in two-way crux.
//
// These probe the ONE gap the existing suite misses: the existing
// TestEnablement_OptInScopedToCaller only covers (a) a body-field org override
// and (b) a fully org-LESS request (nil headers). It never exercises the actual
// Phase-1 forge that SanitizeIdentity leaves open: a request that carries
// X-Org-Id but NO validated principal (no X-User-Id) — the bearer-less
// direct-to-pod path where SanitizeIdentity RESTORES the client's raw X-Org-Id
// while leaving X-User-Id empty (see middleware_identity.go PHASE-1 RESIDUAL and
// clients/principal.Validated).
//
// The read path resolves its tenant through principal.Org (via trustedOrg),
// which refuses that forge. The WRITE path (opt-in/opt-out) keys on the raw
// c.Org() and only checks org != "", so it does NOT.

import (
	"net/http"
	"strings"
	"testing"
)

// TestAttack_OptInForgedOrgNoPrincipal is the PoC: an off-gateway caller with NO
// validated principal (X-Org-Id set, X-User-Id absent) opts an org it does not
// own into a beta. SECURE behavior: refuse (401/403) exactly like the data plane
// (principal.Validated). If this test FAILS, the cross-tenant enablement write is
// live — the opt-in accepts a client-forged tenant.
func TestAttack_OptInForgedOrgNoPrincipal(t *testing.T) {
	do := mountEnablement(t)
	admin := map[string]string{"X-User-IsAdmin": "true"}

	// Admin designates a real beta item.
	if resp, b := do("PUT", "/v1/admin/enablement", `{"kind":"feature","id":"labs","state":"beta"}`, admin); resp.StatusCode != 200 {
		t.Fatalf("admin set beta: %d (%s)", resp.StatusCode, b)
	}

	// The forge: X-Org-Id present, but NO X-User-Id — i.e. principal.Validated
	// is false. This is what SanitizeIdentity produces for a bearer-less caller
	// that simply sends `X-Org-Id: victim` off-gateway.
	forge := map[string]string{"X-Org-Id": "victim"} // no X-User-Id ⇒ not a validated principal

	resp, body := do("POST", "/v1/enablement/optin", `{"kind":"feature","id":"labs"}`, forge)

	// SECURE expectation: an unvalidated principal must not be able to write
	// enablement state for ANY org. The data plane answers 401/403 here.
	if resp.StatusCode == http.StatusOK {
		t.Errorf("VULN: opt-in accepted a forged tenant with no validated principal (status 200, body=%s)", body)
	}

	// Ground truth via the admin registry: did "victim" actually get granted?
	_, lb := do("GET", "/v1/admin/enablement", "", admin)
	if strings.Contains(string(lb), `"victim"`) {
		t.Fatalf("VULN CONFIRMED: an unauthenticated off-gateway caller granted org \"victim\" a beta it does not own — cross-tenant enablement write. registry=%s", lb)
	}
}

// TestAttack_OptOutForgedOrgRevokesGrant is the tamper variant: the same forge
// REMOVES a legitimately-granted org from a beta allow-list (cross-tenant
// denial-of-feature). Admin grants "acme"; the attacker (no principal, forging
// X-Org-Id: acme) opts acme back out.
func TestAttack_OptOutForgedOrgRevokesGrant(t *testing.T) {
	do := mountEnablement(t)
	admin := map[string]string{"X-User-IsAdmin": "true"}

	// Admin puts a beta into place AND grants acme explicitly.
	do("PUT", "/v1/admin/enablement", `{"kind":"feature","id":"labs","state":"beta","betaOrgs":["acme"]}`, admin)

	// Confirm acme is granted.
	_, before := do("GET", "/v1/admin/enablement", "", admin)
	if !strings.Contains(string(before), `"acme"`) {
		t.Fatalf("precondition: acme should be granted, registry=%s", before)
	}

	// The forge: no validated principal, X-Org-Id: acme.
	forge := map[string]string{"X-Org-Id": "acme"} // no X-User-Id
	do("POST", "/v1/enablement/optout", `{"kind":"feature","id":"labs"}`, forge)

	// If acme is gone from the grant list, an unauthenticated caller revoked a
	// legitimate tenant's beta access.
	_, after := do("GET", "/v1/admin/enablement", "", admin)
	if !strings.Contains(string(after), `"acme"`) {
		t.Fatalf("VULN CONFIRMED: an unauthenticated off-gateway caller revoked acme's beta grant (cross-tenant tamper). registry=%s", after)
	}
}
