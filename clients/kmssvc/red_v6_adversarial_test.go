package kmssvc_test

// RED adversarial coverage for the V6 machine-audience widening. Blue's
// v6_aud_e2e_test proves the happy path + simple negatives (cross-tenant,
// owner-mismatched aud, arbitrary aud, expiry, no-cred). It does NOT exercise:
//
//   1. MULTI-VALUE aud where one member is a STATIC-allowlist value and another is
//      a FOREIGN tenant's machine aud (go-jose AnyAudience is an OR/intersection
//      match, so the token VALIDATES via the static member — does owner still
//      strictly govern the reachable org?).
//   2. The ADMIN-ORG machine token: owner == AdminOrg with the machine aud, both
//      isAdmin=false (the real client_credentials shape — IAM's
//      GetClientCredentialsToken builds nullUser with IsAdmin unset) and
//      isAdmin=true (the residual: IF such a token ever exists, does V6 admit it to
//      the global-admin path and thereby read EVERY tenant?).
//   3. A trim-collapsible / unsafe-rune owner in the SIGNED claim (owner "maxpower "
//      must never fold onto tenant "maxpower" end-to-end).
//
// These reuse the SAME real-signed-token pipeline as v6_aud_e2e_test
// (cloud.IdentityMiddleware → real /v1/kms guard); only the claims minter is
// richer (multi-value aud + isAdmin), which Blue's single-aud helper can't express.

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	gojose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/zap-proto/zip"
)

// redClaims extends the machine claims with isAdmin + a multi-value aud so the
// adversarial cases can be constructed. Field JSON shapes match what cloud's
// idClaims reads (owner, isAdmin) and go-jose's registered aud.
type redClaims struct {
	jwt.Claims
	Owner   string `json:"owner"`
	IsAdmin bool   `json:"isAdmin"`
}

// mintRed signs a token with an arbitrary aud SET and an explicit isAdmin, against
// the same kid=test-key the e2e JWKS serves.
func mintRed(t *testing.T, key *rsa.PrivateKey, owner string, aud []string, isAdmin bool, exp time.Time) string {
	t.Helper()
	signer, err := gojose.NewSigner(
		gojose.SigningKey{Algorithm: gojose.RS256, Key: key},
		(&gojose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test-key"),
	)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	raw, err := jwt.Signed(signer).Claims(redClaims{
		Claims: jwt.Claims{
			Issuer:   e2eIssuer,
			Subject:  owner + "/" + owner + "-platform-kms", // non-empty → X-User-Id set
			Audience: jwt.Audience(aud),
			Expiry:   jwt.NewNumericDate(exp),
			IssuedAt: jwt.NewNumericDate(time.Now()),
		},
		Owner:   owner,
		IsAdmin: isAdmin,
	}).Serialize()
	if err != nil {
		t.Fatalf("serialize: %v", err)
	}
	return raw
}

// getBearerHdr issues a bearer GET with additional attacker-supplied request
// headers (e.g. the X-Org-Id admin org-switch input).
func getBearerHdr(t *testing.T, app *zip.App, path, token string, hdr map[string]string) *http.Response {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// ── Vector 1: multi-value aud with a static-allowlist member ────────────────────
//
// acme presents aud = ["hanzo-console" (STATIC-allowlisted), "maxpower-platform-kms"
// (the VICTIM's machine aud)]. AnyAudience OR-matches, so this token VALIDATES via
// "hanzo-console". The attack: the presence of the victim-bound machine aud, or the
// intersection semantics, must NOT let acme reach maxpower. Owner (=acme, signed)
// governs.
func TestRed_MultiValueAud_OwnerStillGoverns(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := e2eJWKS(t, &key.PublicKey)
	app, deps := newAppWithIdentity(t, e2eCfg(t, jwks.URL)) // allowlist = ["hanzo-console"]

	sealPlatformSecret(t, deps.KMS, paasOrgA, paasValueA)       // maxpower's secret
	sealPlatformSecret(t, deps.KMS, paasOrgB, "s3kr3t-of-acme") // acme's own secret
	maxpowerPath := "/v1/kms/orgs/" + paasOrgA + paasEnvPath
	acmePath := "/v1/kms/orgs/" + paasOrgB + paasEnvPath
	future := time.Now().Add(time.Hour)

	multi := mintRed(t, key, paasOrgB, []string{"hanzo-console", paasOrgA + "-platform-kms"}, false, future)

	// Cross-tenant: acme (with maxpower's machine aud in its aud SET) must NOT read maxpower.
	if resp := getWithBearer(t, app, maxpowerPath, multi); resp.StatusCode != 403 {
		t.Fatalf("multi-value aud [hanzo-console, maxpower-platform-kms] owner=acme → maxpower = %d, want 403", resp.StatusCode)
	}
	// Proves the token DID validate (so the 403 above is the org boundary, not a
	// validation reject): the same token reads ACME's own secret → 200.
	if resp := getWithBearer(t, app, acmePath, multi); resp.StatusCode != 200 {
		t.Fatalf("multi-value aud owner=acme → acme (own) = %d, want 200 (token must have validated)", resp.StatusCode)
	}
}

// ── Vector 2: admin-org machine token ───────────────────────────────────────────
//
// (a) The REAL client_credentials shape: owner == AdminOrg ("admin"), aud ==
//
//	"admin-platform-kms", isAdmin=FALSE. V6 makes this VALIDATE (machine aud). It
//	must NOT thereby become global admin — owner==adminOrg ALONE is not admin; the
//	code requires isAdmin=true. So it can read only the admin org's own secrets.
//
// (b) The RESIDUAL: the SAME token but isAdmin=TRUE. This models an isAdmin-bearing
//
//	token whose ONLY audience is the per-tenant machine aud (not in the static
//	allowlist) — pre-V6 that 403s at validation; POST-V6 the machine-aud
//	acceptance admits it to the global-admin path and it reads EVERY tenant. The
//	in-binary code does NOT defend against this; the sole barrier is the external
//	invariant "IAM never stamps isAdmin=true on a machine-aud token."
func TestRed_AdminOrgMachineToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := e2eJWKS(t, &key.PublicKey)
	app, deps := newAppWithIdentity(t, e2eCfg(t, jwks.URL)) // AdminOrg="admin", allowlist=["hanzo-console"]

	sealPlatformSecret(t, deps.KMS, paasOrgA, paasValueA)       // victim maxpower secret
	sealPlatformSecret(t, deps.KMS, "admin", "s3kr3t-of-admin") // admin org's own secret
	victimPath := "/v1/kms/orgs/" + paasOrgA + paasEnvPath
	adminPath := "/v1/kms/orgs/admin" + paasEnvPath
	future := time.Now().Add(time.Hour)

	// (a) isAdmin=FALSE — the real client_credentials machine token.
	ccAdmin := mintRed(t, key, "admin", []string{"admin-platform-kms"}, false, future)

	// It CAN read the admin org's OWN secrets (legit principal for its own org).
	if resp := getWithBearer(t, app, adminPath, ccAdmin); resp.StatusCode != 200 {
		t.Fatalf("admin-org machine token → admin-org own secret = %d, want 200", resp.StatusCode)
	}
	// It must NOT read a DIFFERENT tenant — owner==adminOrg without isAdmin is not global admin.
	if resp := getWithBearer(t, app, victimPath, ccAdmin); resp.StatusCode != 403 {
		t.Fatalf("admin-org MACHINE token (isAdmin=false) → victim = %d, want 403 (no global admin from owner alone)", resp.StatusCode)
	}
	// Not even with an explicit org-switch header (admin org-switch is honored ONLY for isAdmin=true).
	if resp := getBearerHdr(t, app, victimPath, ccAdmin, map[string]string{"X-Org-Id": paasOrgA}); resp.StatusCode != 403 {
		t.Fatalf("admin-org machine token + X-Org-Id:maxpower switch = %d, want 403", resp.StatusCode)
	}

	// (b) RESIDUAL — NOW CLOSED by Blue's fix (isKMSMachinePrincipal): isAdmin=TRUE,
	//     aud == the machine aud ONLY (NOT in the static allowlist).
	//     Contrast probe: isAdmin=true with an ARBITRARY aud is 403 (validation never
	//     admits it, so isAdmin is never consulted).
	arbAdminTrue := mintRed(t, key, "admin", []string{"some-random-app"}, true, future)
	if resp := getWithBearer(t, app, victimPath, arbAdminTrue); resp.StatusCode != 403 {
		t.Fatalf("isAdmin=true + arbitrary aud → victim = %d, want 403 (not admitted)", resp.StatusCode)
	}
	//     Swap the arbitrary aud for the machine aud: the token now VALIDATES (V6), but
	//     SanitizeIdentity denies global admin to a MACHINE principal, so it is pinned to
	//     owner=admin and CANNOT read the victim → 403 (was 200 pre-fix — residual closed).
	machAdminTrue := mintRed(t, key, "admin", []string{"admin-platform-kms"}, true, future)
	if resp := getWithBearer(t, app, victimPath, machAdminTrue); resp.StatusCode != 403 {
		t.Fatalf("RESIDUAL must be CLOSED: isAdmin=true + machine aud → victim = %d, want 403 "+
			"(a machine principal must NEVER receive global admin)", resp.StatusCode)
	}
	//     The fix gates ONLY the admin grant: the machine principal still reads its OWN
	//     org (admin), so data-plane access is intact.
	if resp := getWithBearer(t, app, adminPath, machAdminTrue); resp.StatusCode != 200 {
		t.Fatalf("admin-org machine principal → admin-org own secret = %d, want 200 (data access intact)", resp.StatusCode)
	}
}

// ── Vector 3: trim-collapsible / unsafe-rune owner in the SIGNED claim ───────────
//
// owner "maxpower " (trailing space) with a matching machine aud so validate()
// passes. SanitizeIdentity must REFUSE to fold "maxpower " onto tenant "maxpower":
// OrgHasUnsafeRune zeroes the owner → org-less → the guard 403s the victim read.
func TestRed_TrimCollapseOwner_FailsClosed(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := e2eJWKS(t, &key.PublicKey)
	app, deps := newAppWithIdentity(t, e2eCfg(t, jwks.URL))
	sealPlatformSecret(t, deps.KMS, paasOrgA, paasValueA) // maxpower
	victimPath := "/v1/kms/orgs/" + paasOrgA + paasEnvPath
	future := time.Now().Add(time.Hour)

	for _, owner := range []string{"maxpower ", "maxpower​", " maxpower", "maxpower\t"} {
		// aud is bound to the RAW owner so validate()'s machine-aud check passes; the
		// defense must be SanitizeIdentity refusing tenancy for the unsafe owner.
		tok := mintRed(t, key, owner, []string{owner + "-platform-kms"}, false, future)
		if resp := getWithBearer(t, app, victimPath, tok); resp.StatusCode != 403 {
			t.Fatalf("unsafe/trim owner %q reading maxpower = %d, want 403 (no fold onto victim)", owner, resp.StatusCode)
		}
	}

	// Empty owner with the bare-suffix aud: kmsMachineAudience("")=="" so no machine
	// aud is added; the bare "-platform-kms" is not in the allowlist → anonymous → 403.
	empty := mintRed(t, key, "", []string{"-platform-kms"}, false, future)
	if resp := getWithBearer(t, app, victimPath, empty); resp.StatusCode != 403 {
		t.Fatalf("empty-owner bare-suffix aud = %d, want 403 (fail closed)", resp.StatusCode)
	}
}
