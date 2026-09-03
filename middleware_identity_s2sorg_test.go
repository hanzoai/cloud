// Copyright © 2026 Hanzo AI. MIT License.

package cloud

import (
	"net/http"
	"testing"
)

// A client's own org survives sanitization for the data path — and NOTHING else
// does. This was learned the expensive way: an opaque bearer's X-Org-Id was once
// stripped along with the identity headers, billing answered 401 "sign in to view
// billing", and because the balance gate is fail-CLOSED that denied EVERY paid
// completion fleet-wide on a pod whose commerce subsystem was healthy.
//
// The org must survive — and NOTHING else may.
//
// This asserts the BOUNDARY, not a copy of it. The version it replaces re-implemented
// the bearer compare in the test file and asserted the copy, so it passed whatever the
// middleware did — it could not have caught the sanitizer dropping the org, which is
// the one thing it was written to catch.
func TestAnOpaqueBearerKeepsItsOrgAndGainsNoAuthority(t *testing.T) {
	const tok = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

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
		t.Errorf("an opaque bearer minted a user: %q", got.user)
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

	app, got := newIdentityApp(t, nil)
	probe(t, app, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+tok)
		r.Header.Set("X-Org-Id", "han​zo")
	})

	if got.org != "" {
		t.Errorf("an org bearing a zero-width rune was scoped to %q", got.org)
	}
}
