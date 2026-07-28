package kms_test

// RED RE-VERIFY (2nd cycle) of the fix commit 829b6ed5 — decouple V6 machine-aud
// from SuperAdmin. This file FOCUS-FIRES the one surface the fix newly introduces:
// the interaction of isKMSMachinePrincipal (deny-admin gate) with go-jose's
// AnyAudience OR-match. If a MULTI-VALUE aud carrying BOTH a static-allowlist member
// AND the owner's machine aud could "slip" back to SuperAdmin (because the static
// member is what let it validate), the fix would have a hole.
//
// THE ORACLE CHANGED WITH THE ORG RESHAPE; THE QUESTION DID NOT. This file used to
// read "did the token get SuperAdmin?" off a cross-org URL — a real admin reading
// /v1/kms/orgs/{victim}/… got 200, a pinned machine principal got 403. No route
// names an org any more, so that oracle returns nothing about admin at all: every
// caller now spells one path and is served ITS OWN org, which is why the old
// assertions were firing 403-expected on a 200 that carried the ADMIN'S OWN secret.
//
// What SuperAdmin actually confers today is the CLAIM-BOUND ORG-SWITCH:
// SanitizeIdentity's admin arm — and only that arm — honors X-Org-Id as the
// effective org (middleware_identity.go `effOrg = cliOrg`), gated on membership of
// the reserved admin org AND !isMachinePrincipal. So the probe is the same request
// with `X-Org-Id: maxpower` attached, and the observable is the VALUE returned:
//
//	victim's plaintext   ⟹ the token wields platform sudo
//	admin's own plaintext ⟹ admin was denied; the switch was inert
//
// That is strictly sharper than the status it replaces — a 403 could have come from
// any refusal, while a returned secret names exactly which tenant was selected.
//
// Harness (e2eCfg): AdminOrg="admin"; audience is not gated (trust = signature +
// issuer + expiry). Reuses mintRed / getWithBearer / getBearerHdr /
// sealPlatformSecret from the e2e + red_v6_adversarial files (same kms_test package).

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// adminSlipWorld seals the victim's and the admin org's secrets at the IDENTICAL
// coordinate — the one path every caller spells — and returns it alongside the
// org-switch header the SuperAdmin arm consumes. Distinct plaintexts at one
// coordinate are what make the oracle unambiguous.
const (
	slipVictimValue = paasValueA           // maxpower's
	slipAdminValue  = "s3kr3t-of-admin"    // the admin org's own
	slipStaticAud   = "hanzo-console"      // a static-allowlist member: makes the token validate
	slipOwnMachAud  = "admin-platform-kms" // the OWNER's machine aud: must strip admin
)

func adminSlipWorld(t *testing.T) (app *zip.App, key *rsa.PrivateKey, path string, sw map[string]string) {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := e2eJWKS(t, &k.PublicKey)
	a, deps := newAppWithIdentity(t, e2eCfg(t, jwks.URL)) // AdminOrg="admin", allowlist=["hanzo-console"]
	sealPlatformSecret(t, deps.KMS, paasOrgA, slipVictimValue)
	sealPlatformSecret(t, deps.KMS, "admin", slipAdminValue)
	return a, k, "/v1/kms" + paasEnvPath, map[string]string{"X-Org-Id": paasOrgA}
}

// slipValue reads the plaintext a probe was served — the whole observable.
func slipValue(t *testing.T, resp *http.Response) any {
	t.Helper()
	return decode(t, resp.Body)["value"]
}

// TestRed_MultiValueAud_AdminSlip is the focus-fire: owner==adminOrg, isAdmin=true,
// aud = [ <static allowlist member>, <owner machine aud> ]. The token VALIDATES via
// the static member (AnyAudience OR), so the fix cannot rely on validation rejecting
// it — it must rely on isKMSMachinePrincipal firing on the machine aud's PRESENCE and
// stripping admin. Proven by contrast with a real admin (no machine aud) that KEEPS
// its cross-org switch.
func TestRed_MultiValueAud_AdminSlip(t *testing.T) {
	app, key, path, switchToVictim := adminSlipWorld(t)
	future := time.Now().Add(time.Hour)

	// ── BASELINE: a REAL SuperAdmin — isAdmin=true, aud=[hanzo-console] ONLY (no
	//    machine aud). isKMSMachinePrincipal("admin")=false → SuperAdmin GRANTED →
	//    the org-switch is honored → the VICTIM'S value comes back. This pins the
	//    oracle: the victim's plaintext here means "SuperAdmin reaches a foreign
	//    org", so the same plaintext on the attack tokens below would be the SLIP,
	//    and it also proves the fix does NOT over-block a legitimate admin that
	//    merely carries other audiences.
	realAdmin := mintRed(t, key, "admin", []string{slipStaticAud}, true, future)
	if got := slipValue(t, getBearerHdr(t, app, path, realAdmin, switchToVictim)); got != slipVictimValue {
		t.Fatalf("REAL admin (aud=[%s], isAdmin=true) + org-switch read %v, want %q "+
			"(SuperAdmin must still reach a foreign org; anything else is an over-block regression)",
			slipStaticAud, got, slipVictimValue)
	}

	// ── ATTACK (the focus-fire): owner=admin, isAdmin=true, aud = [ hanzo-console
	//    (STATIC allowlist), admin-platform-kms (the OWNER machine aud) ]. AnyAudience
	//    OR-matches "hanzo-console" so the token VALIDATES (the machine widening was not
	//    even needed). The presence of the owner machine aud MUST still trip
	//    isKMSMachinePrincipal → deny SuperAdmin → org-pinned to "admin" → the switch is
	//    inert and the ADMIN'S OWN value comes back. The victim's value would mean the
	//    static co-member let it slip back to admin: the fix would be BYPASSED.
	slip := mintRed(t, key, "admin", []string{slipStaticAud, slipOwnMachAud}, true, future)
	if got := slipValue(t, getBearerHdr(t, app, path, slip, switchToVictim)); got != slipAdminValue {
		t.Fatalf("ADMIN-SLIP: aud=[%s, %s] isAdmin=true owner=admin + org-switch read %v, want %q "+
			"(machine-aud presence must deny SuperAdmin even with a static-allowlist co-member)",
			slipStaticAud, slipOwnMachAud, got, slipAdminValue)
	}

	// Prove the slip token DID validate (so the denial above is the admin-deny + org-pin,
	// NOT a validation reject): the SAME token, with no switch header, reads its OWN org.
	// This is the data-plane-intact half — the fix gates ONLY the admin grant.
	if got := slipValue(t, getWithBearer(t, app, path, slip)); got != slipAdminValue {
		t.Fatalf("multi-value machine token → own org read %v, want %q "+
			"(token must have validated; org-scoped data access must remain intact)", got, slipAdminValue)
	}

	// Order-independence: reverse the aud so the machine aud is FIRST.
	// isKMSMachinePrincipal scans the whole set, so the deny must not depend on ordering.
	slipRev := mintRed(t, key, "admin", []string{slipOwnMachAud, slipStaticAud}, true, future)
	if got := slipValue(t, getBearerHdr(t, app, path, slipRev, switchToVictim)); got != slipAdminValue {
		t.Fatalf("ADMIN-SLIP (reversed aud order [%s, %s]) read %v, want %q",
			slipOwnMachAud, slipStaticAud, got, slipAdminValue)
	}

	// The victim's record is intact and still reachable BY THE VICTIM — so the denials
	// above are the admin gate, not a broken endpoint.
	victimTok := mintRed(t, key, paasOrgA, []string{slipStaticAud}, false, future)
	if got := slipValue(t, getWithBearer(t, app, path, victimTok)); got != slipVictimValue {
		t.Fatalf("victim reading its OWN secret got %v, want %q", got, slipVictimValue)
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
	app, key, path, switchToVictim := adminSlipWorld(t)
	future := time.Now().Add(time.Hour)

	// owner=admin, isAdmin=true, aud=[hanzo-console (static), maxpower-platform-kms (FOREIGN
	// machine aud)]. Not the owner's machine aud → isKMSMachinePrincipal(admin)=false →
	// real SuperAdmin → the switch is honored → the victim's value.
	fa := mintRed(t, key, "admin", []string{slipStaticAud, paasOrgA + "-platform-kms"}, true, future)
	if got := slipValue(t, getBearerHdr(t, app, path, fa, switchToVictim)); got != slipVictimValue {
		t.Fatalf("real admin carrying a FOREIGN machine aud + org-switch read %v, want %q "+
			"(a foreign machine aud must NOT strip admin; that would be an over-block)", got, slipVictimValue)
	}

	// Contrast — the DISCRIMINATOR is the owner's OWN machine aud, not any machine aud:
	// swap the foreign maxpower-platform-kms for admin's OWN admin-platform-kms and the
	// SAME shape becomes a machine principal → isKMSMachinePrincipal fires → admin
	// stripped → the switch is inert and the ADMIN'S own value comes back. So a FOREIGN
	// machine aud keeps admin (fa above); the OWN machine aud strips it — owner-bound.
	ownMach := mintRed(t, key, "admin", []string{slipStaticAud, slipOwnMachAud}, true, future)
	if got := slipValue(t, getBearerHdr(t, app, path, ownMach, switchToVictim)); got != slipAdminValue {
		t.Fatalf("owner=admin carrying its OWN machine aud + org-switch read %v, want %q "+
			"(own machine aud strips admin)", got, slipAdminValue)
	}
}
