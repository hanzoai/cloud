// Copyright © 2026 Hanzo AI. MIT License.

package cloud

import (
	"net/http"
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
//
// This asserts the BOUNDARY, not a copy of it. The version it replaces re-implemented
// the token compare in the test file and asserted the copy, so it passed whatever the
// middleware did — it could not have caught the sanitizer dropping the org, which is
// the one thing it was written to catch.
func TestTrustedServiceTokenKeepsItsOrgAndGainsNoAuthority(t *testing.T) {
	const tok = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("COMMERCE_SERVICE_TOKEN", tok)

	app, got := newIdentityApp(t, nil)
	probe(t, app, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("X-Org-Id", "hanzo")
	})

	if got.org != "hanzo" {
		t.Fatalf("the in-proc caller's org did not survive sanitization: %q — that empty "+
			"org is the 401 billing answered, and the fleet-wide 503 behind it", got.org)
	}
	// Surviving is not a grant: no user, no admin, no org-admin.
	if got.user != "" {
		t.Errorf("a service token minted a user: %q", got.user)
	}
	if got.admin || got.orgAdmin {
		t.Errorf("a service token gained admin authority (admin=%v orgAdmin=%v)", got.admin, got.orgAdmin)
	}
}

// TestClientOrgIsNotAnIdentity is the other half. Every anonymous caller's X-Org-Id
// rides the same unconditional restore, so the header alone must never read as a
// principal — the surfaces gate on a validated one.
func TestClientOrgIsNotAnIdentity(t *testing.T) {
	app, got := newIdentityApp(t, nil)
	probe(t, app, func(r *http.Request) { r.Header.Set("X-Org-Id", "acme") })

	if got.user != "" || got.admin || got.orgAdmin {
		t.Errorf("an anonymous X-Org-Id granted identity: user=%q admin=%v orgAdmin=%v",
			got.user, got.admin, got.orgAdmin)
	}
}

// TestOrgWithAnUnsafeRuneIsRefused pins the anti-forgery the restore must not loosen:
// an org carrying a format rune is a non-injective identifier (folding it would put two
// distinct IAM orgs in one namespace), so it grants NO scoping at all. A zero-width
// space rather than a plain one, because the transport OWS-trims the plain one before
// the boundary ever sees it — the invisible rune is the one that reaches here.
func TestOrgWithAnUnsafeRuneIsRefused(t *testing.T) {
	const tok = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("COMMERCE_SERVICE_TOKEN", tok)

	app, got := newIdentityApp(t, nil)
	probe(t, app, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("X-Org-Id", "han​zo")
	})

	if got.org != "" {
		t.Errorf("an org bearing a zero-width rune was scoped to %q", got.org)
	}
}
