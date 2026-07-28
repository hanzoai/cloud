package kms_test

// RED RE-VERIFY (2nd cycle) of the fix commit 829b6ed5 — decouple V6 machine-aud
// from SuperAdmin. This file FOCUS-FIRES the one surface the fix newly introduces:
// the interaction of isKMSMachinePrincipal (deny-admin gate) with go-jose's
// AnyAudience OR-match. If a MULTI-VALUE aud carrying BOTH a static-allowlist member
// AND the owner's machine aud could "slip" back to SuperAdmin (because the static
// member is what let it validate), the fix would have a hole.
//
// The end-to-end oracle is crisp because svc.guard() (mount.go) grants a
// SuperAdmin cross-org reads:  `if !ctx.IsAdmin() && ctx.Org() != org → 403`.
//   - a REAL SuperAdmin reading the victim's path  → 200 (cross-org allowed)
//   - a machine principal DENIED admin (org-pinned)  → 403 (cross-org denied)
// So a 200 on victimPath == "the token got SuperAdmin"; 403 == "admin denied".
// The whole test reduces the slip question to a single observable status code.
//
// Harness (e2eCfg): AdminOrg="admin"; audience is not gated (trust = signature+issuer+expiry).
// Reuses mintRed / getWithBearer / getBearerHdr / sealPlatformSecret from the e2e +
// red_v6_adversarial files (same kms_test package).

import (
	"crypto/rand"
	"crypto/rsa"
	"testing"
	"time"
)

// TestRed_MultiValueAud_AdminSlip is the focus-fire: owner==adminOrg, isAdmin=true,
// aud = [ <static allowlist member>, <owner machine aud> ]. The token VALIDATES via
// the static member (AnyAudience OR), so the fix cannot rely on validation rejecting
// it — it must rely on isKMSMachinePrincipal firing on the machine aud's PRESENCE and
// stripping admin. Proven by contrast with a real admin (no machine aud) that KEEPS
// its cross-org read.
func TestRed_MultiValueAud_AdminSlip(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := e2eJWKS(t, &key.PublicKey)
	app, deps := newAppWithIdentity(t, e2eCfg(t, jwks.URL)) // AdminOrg="admin", allowlist=["hanzo-console"]

	sealPlatformSecret(t, deps.KMS, paasOrgA, paasValueA)       // victim maxpower secret
	sealPlatformSecret(t, deps.KMS, "admin", "s3kr3t-of-admin") // admin org's own secret
	victimPath := "/v1/kms" + paasEnvPath
	adminPath := "/v1/kms/orgs/admin" + paasEnvPath
	future := time.Now().Add(time.Hour)

	// ── BASELINE: a REAL SuperAdmin — isAdmin=true, aud=[hanzo-console] ONLY (no
	//    machine aud). isKMSMachinePrincipal("admin")=false → SuperAdmin GRANTED →
	//    the guard admits the cross-org read → 200. This pins the oracle: 200 here
	//    means "SuperAdmin reaches a foreign org", so a 200 on the attack tokens
	//    below would be the SLIP, and it also proves the fix does NOT over-block a
	//    legitimate admin that merely carries other audiences.
	realAdmin := mintRed(t, key, "admin", []string{"hanzo-console"}, true, future)
	if resp := getWithBearer(t, app, victimPath, realAdmin); resp.StatusCode != 200 {
		t.Fatalf("REAL admin (aud=[hanzo-console], isAdmin=true) → victim = %d, want 200 "+
			"(SuperAdmin must still cross-org read; a 403 here would be an over-block regression)", resp.StatusCode)
	}

	// ── ATTACK (the focus-fire): owner=admin, isAdmin=true, aud = [ hanzo-console
	//    (STATIC allowlist), admin-platform-kms (the OWNER machine aud) ]. AnyAudience
	//    OR-matches "hanzo-console" so the token VALIDATES (the machine widening was not
	//    even needed). The presence of the owner machine aud MUST still trip
	//    isKMSMachinePrincipal → deny SuperAdmin → org-pinned to "admin" → the guard
	//    denies the cross-org read → 403. A 200 would mean the static co-member let it
	//    slip back to admin: the fix would be BYPASSED.
	slip := mintRed(t, key, "admin", []string{"hanzo-console", "admin-platform-kms"}, true, future)
	if resp := getWithBearer(t, app, victimPath, slip); resp.StatusCode != 403 {
		t.Fatalf("ADMIN-SLIP: aud=[hanzo-console, admin-platform-kms] isAdmin=true owner=admin → victim = %d, "+
			"want 403 (machine-aud presence must deny SuperAdmin even with a static-allowlist co-member)", resp.StatusCode)
	}

	// Prove the slip token DID validate (so the 403 is the admin-deny + org-pin, NOT a
	// validation reject): the SAME token reads its OWN org (admin) → 200. This is the
	// data-plane-intact half — the fix gates ONLY the admin grant.
	if resp := getWithBearer(t, app, adminPath, slip); resp.StatusCode != 200 {
		t.Fatalf("multi-value machine token → admin own secret = %d, want 200 "+
			"(token must have validated; org-scoped data access must remain intact)", resp.StatusCode)
	}

	// Order-independence: reverse the aud so the machine aud is FIRST. isKMSMachinePrincipal
	// scans the whole set, so the deny must not depend on aud ordering.
	slipRev := mintRed(t, key, "admin", []string{"admin-platform-kms", "hanzo-console"}, true, future)
	if resp := getWithBearer(t, app, victimPath, slipRev); resp.StatusCode != 403 {
		t.Fatalf("ADMIN-SLIP (reversed aud order [admin-platform-kms, hanzo-console]) → victim = %d, want 403", resp.StatusCode)
	}

	// The admin org-SWITCH header must also be inert for the machine principal (the
	// switch is honored ONLY inside the admin-grant case, which the machine principal
	// no longer enters). Explicit X-Org-Id:maxpower must NOT redirect it to the victim.
	if resp := getBearerHdr(t, app, victimPath, slip, map[string]string{"X-Org-Id": paasOrgA}); resp.StatusCode != 403 {
		t.Fatalf("ADMIN-SLIP + X-Org-Id:maxpower org-switch → victim = %d, want 403 (machine principal cannot org-switch)", resp.StatusCode)
	}
	// Even switching to admin's own org via header changes nothing about cross-org:
	// the machine principal reading the victim path is still 403 regardless of header.
	if resp := getBearerHdr(t, app, adminPath, slip, map[string]string{"X-Org-Id": paasOrgA}); resp.StatusCode != 200 {
		t.Fatalf("machine principal + X-Org-Id:maxpower reading its OWN adminPath = %d, want 200 "+
			"(header switch is inert; own-org data access unaffected)", resp.StatusCode)
	}
}

// TestRed_ForeignMachineAudInSet_RealAdminKept documents the boundary of the gate:
// a real admin whose aud carries a FOREIGN tenant's machine aud (NOT its own) must
// KEEP SuperAdmin. kmsMachineAudience(owner="admin")="admin-platform-kms"; the set
// carries "maxpower-platform-kms", which is NOT the owner's machine aud, so
// isKMSMachinePrincipal returns false and admin is retained. This is CORRECT: the
// admin-deny gate is OWNER-BOUND — it fires only on the owner's own machine aud, so a
// foreign machine aud in the set never strips a bona-fide admin. Denying it would be
// an over-block that breaks multi-aud admin tokens.
func TestRed_ForeignMachineAudInSet_RealAdminKept(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := e2eJWKS(t, &key.PublicKey)
	app, deps := newAppWithIdentity(t, e2eCfg(t, jwks.URL))
	sealPlatformSecret(t, deps.KMS, paasOrgA, paasValueA)
	victimPath := "/v1/kms" + paasEnvPath
	future := time.Now().Add(time.Hour)

	// owner=admin, isAdmin=true, aud=[hanzo-console (static), maxpower-platform-kms (FOREIGN machine aud)].
	// Not the owner's machine aud → isKMSMachinePrincipal(admin)=false → real SuperAdmin → 200.
	fa := mintRed(t, key, "admin", []string{"hanzo-console", paasOrgA + "-platform-kms"}, true, future)
	if resp := getWithBearer(t, app, victimPath, fa); resp.StatusCode != 200 {
		t.Fatalf("real admin carrying a FOREIGN machine aud → victim = %d, want 200 "+
			"(a foreign machine aud must NOT strip admin; that would be an over-block)", resp.StatusCode)
	}

	// Contrast — the DISCRIMINATOR is the owner's OWN machine aud, not any machine aud:
	// swap the foreign maxpower-platform-kms for admin's OWN admin-platform-kms and the
	// SAME shape becomes a machine principal → isKMSMachinePrincipal fires → admin stripped
	// → victim read 403. So a FOREIGN machine aud keeps admin (fa above); the OWN machine
	// aud strips it — the gate is owner-bound.
	ownMach := mintRed(t, key, "admin", []string{"hanzo-console", "admin-platform-kms"}, true, future)
	if resp := getWithBearer(t, app, victimPath, ownMach); resp.StatusCode != 403 {
		t.Fatalf("owner=admin carrying its OWN machine aud → victim = %d, want 403 (own machine aud strips admin)", resp.StatusCode)
	}
}
