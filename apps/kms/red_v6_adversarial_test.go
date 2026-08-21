package kms_test

// THE MECHANISM MOVED; THE QUESTION AND THE ANSWER DID NOT. This file was written
// when a machine was RECOGNISED by an owner-bound audience and had admin subtracted
// afterwards, so it asks whether a multi-value audience could slip past that
// recognition. IAM now signs the kind (`type: application`) and Sudo refuses every
// machine, so the audience decides nothing and there is nothing for an audience to
// slip past. The assertions below still hold, for that reason rather than the old
// one; the reasoning quoted in the comments describes the mechanism they replaced.
//
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
//      the SuperAdmin path and thereby read EVERY tenant?).
//   3. A trim-collapsible / unsafe-rune owner in the SIGNED claim (owner "maxpower "
//      must never fold onto tenant "maxpower" end-to-end).
//
// These reuse the SAME real-signed-token pipeline as v6_aud_e2e_test
// (cloud.IdentityMiddleware → real /v1/kms guard); only the claims minter is
// richer (multi-value aud + isAdmin), which Blue's single-aud helper can't express.

import (
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gojose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/hanzoai/authz"
	model "github.com/hanzoai/iam/pkg/model"
	"github.com/zap-proto/zip"
)

// redClaims extends the machine claims with isAdmin + a multi-value aud so the
// adversarial cases can be constructed. Field JSON shapes match what cloud's
// idClaims reads (owner, isAdmin) and go-jose's registered aud.
type redClaims struct {
	jwt.Claims
	Owner   string         `json:"owner"`
	IsAdmin bool           `json:"isAdmin"`
	Orgs    []model.OrgRef `json:"orgs"` // membership SET, home org first — what IAM mints for a USER.
	// Type is the KIND, and it is what decides machine-ness now: IAM signs
	// `type: application` for a client_credentials token and authz.Claims.Machine
	// reads it. Without this field a mint here could not express a machine at all
	// — it carried an owner, a membership set and a machine-shaped audience, which
	// is a HUMAN by every term the predicate has — so the probes that assert a
	// machine is denied admin were handing the gate a human and reading the human
	// answer back.
	Type string `json:"type,omitempty"`
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
		// THE KIND IS SIGNED, and this suite already declares it: `<owner>-platform-kms`
		// is how every probe here says "this org's OWN machine". Translating that into
		// the claim IAM actually mints is what makes a machine probe a machine — the
		// audience itself decides nothing any more.
		//
		// Owner-bound deliberately. A token carrying ANOTHER org's machine audience
		// (acme presenting maxpower-platform-kms) is not acme's machine, and several
		// probes exist precisely to show that borrowing an audience confers nothing.
		// Those stay human, which is what makes their answer meaningful.
		Type: machineKind(owner, aud),
		// SuperAdmin is HOME-ORG MEMBERSHIP (middleware_identity.go SanitizeIdentity):
		// homeOrg == adminOrg and human. `owner` names the APP's org, not the user's,
		// and isAdmin is deliberately not a term. A human token therefore has to carry
		// the orgs claim or it is nobody, which is why these mints stopped granting
		// admin when the gate moved off `owner`.
		Orgs: []model.OrgRef{{Org: owner}},
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
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

// ── Vector 1: multi-value aud carrying a victim's machine aud ────────────────────
//
// acme presents aud = ["hanzo-console", "maxpower-platform-kms" (the VICTIM's machine
// aud)]. The token validates (audience is not a gate). The attack: the presence of the
// victim-bound machine aud in the set must NOT let acme reach maxpower. Owner (=acme,
// signed) governs.
//
// THE ORACLE MOVED FROM STATUS TO VALUE. This vector used to read the victim by
// naming it in the URL, so "denied" was observable as a 403. There is no such URL:
// both tenants spell one path and the org comes from the signed claim, so the
// request ALWAYS succeeds — the only question is WHOSE record it returns. Both orgs
// are seeded at the identical coordinate with distinct plaintexts, and the
// assertion is that acme's token yields acme's bytes. That is a strictly sharper
// test of "owner governs, not aud": the old 403 could have come from any refusal,
// while a returned plaintext names exactly which tenant the boundary selected.
func TestRed_MultiValueAud_OwnerStillGoverns(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := e2eJWKS(t, &key.PublicKey)
	app, deps := newAppWithIdentity(t, e2eCfg(t, jwks.URL)) // allowlist = ["hanzo-console"]

	const acmeValue = "s3kr3t-of-acme"
	sealPlatformSecret(t, deps.KMS, paasOrgA, paasValueA) // maxpower's secret
	sealPlatformSecret(t, deps.KMS, paasOrgB, acmeValue)  // acme's own, SAME coordinate
	path := "/v1/kms" + paasEnvPath                       // the ONE path both tenants use
	future := time.Now().Add(time.Hour)

	multi := mintRed(t, key, paasOrgB, []string{"hanzo-console", paasOrgA + "-platform-kms"}, false, future)

	// acme, carrying maxpower's machine aud, is served ACME's record.
	resp := getWithBearer(t, app, path, multi)
	if resp.StatusCode != 200 {
		t.Fatalf("multi-value aud owner=acme = %d, want 200 (the token must validate; aud is not a gate)", resp.StatusCode)
	}
	if got := decode(t, resp.Body)["value"]; got != acmeValue {
		t.Fatalf("AUD WIDENED REACH: aud [hanzo-console, maxpower-platform-kms] owner=acme read %v, "+
			"want %q — the victim's machine aud must not select the victim's record", got, acmeValue)
	}

	// Order-independence: the victim's machine aud FIRST changes nothing.
	rev := mintRed(t, key, paasOrgB, []string{paasOrgA + "-platform-kms", "hanzo-console"}, false, future)
	if got := decode(t, getWithBearer(t, app, path, rev).Body)["value"]; got != acmeValue {
		t.Fatalf("AUD WIDENED REACH (reversed aud order): read %v, want %q", got, acmeValue)
	}

	// Nor can the aud be combined with an org SELECTION: acme is not a member of
	// maxpower, so SanitizeIdentity discards the switch and acme stays in acme.
	if got := decode(t, getBearerHdr(t, app, path, multi, map[string]string{"X-Org-Id": paasOrgA}).Body)["value"]; got != acmeValue {
		t.Fatalf("AUD WIDENED REACH (aud + X-Org-Id switch): read %v, want %q", got, acmeValue)
	}

	// The converse pins that this is about the owner and not about acme being
	// somehow special: maxpower's token still reads maxpower.
	if got := decode(t, getWithBearer(t, app, path, mintRed(t, key, paasOrgA, []string{"hanzo-console"}, false, future)).Body)["value"]; got != paasValueA {
		t.Fatalf("owner=maxpower read %v, want %q", got, paasValueA)
	}
}

// ── Vector 2: admin-org machine token ───────────────────────────────────────────
//
// (a) The REAL client_credentials shape: owner == AdminOrg ("admin"), aud ==
//
//	"admin-platform-kms", isAdmin=FALSE. V6 makes this VALIDATE (machine aud). It
//	must NOT thereby become SuperAdmin — owner==adminOrg ALONE is not admin; the
//	code requires isAdmin=true. So it can read only the admin org's own secrets.
//
// (b) The machine-principal exception: the SAME owner==AdminOrg but isAdmin=TRUE. A
//
//	real admin is SuperAdmin from any app (audience is not a gate), but a MACHINE
//	principal — identified by its OWN <owner>-platform-kms aud — is DENIED SuperAdmin
//	by the machine kind and pinned to its own org. A client_credentials machine
//	identity must never wield platform-admin.
//
// THE ORACLE FOR "GOT SUPERADMIN" IS THE ORG-SWITCH, NOT A URL. Reaching a foreign
// org used to mean naming it in the path; that route is gone. What SuperAdmin
// actually confers now is the CLAIM-BOUND org-switch — SanitizeIdentity's admin arm
// alone honors X-Org-Id as the effective org (middleware_identity.go `effOrg =
// cliOrg`). So the test presents the switch header and reads the VALUE: maxpower's
// plaintext means the token got platform sudo, admin's own means it did not. Same
// question, an oracle that still exists, and a sharper answer than a status.
func TestRed_AdminOrgMachineToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := e2eJWKS(t, &key.PublicKey)
	app, deps := newAppWithIdentity(t, e2eCfg(t, jwks.URL)) // AdminOrg="admin", allowlist=["hanzo-console"]

	const adminValue = "s3kr3t-of-admin"
	sealPlatformSecret(t, deps.KMS, paasOrgA, paasValueA) // victim maxpower secret
	sealPlatformSecret(t, deps.KMS, "admin", adminValue)  // admin org's own, SAME coordinate
	path := "/v1/kms" + paasEnvPath                       // the ONE path; the token picks the tenant
	switchToVictim := map[string]string{"X-Org-Id": paasOrgA}
	future := time.Now().Add(time.Hour)

	// value reads one probe's plaintext — the observable that says which tenant the
	// boundary selected.
	value := func(resp *http.Response) any { return decode(t, resp.Body)["value"] }

	// (a) isAdmin=FALSE — the real client_credentials machine token.
	ccAdmin := mintRed(t, key, "admin", []string{"admin-platform-kms"}, false, future)

	// It CAN read the admin org's OWN secrets (legit principal for its own org).
	if got := value(getWithBearer(t, app, path, ccAdmin)); got != adminValue {
		t.Fatalf("admin-org machine token → own org read %v, want %q", got, adminValue)
	}
	// It must NOT reach a DIFFERENT tenant — owner==adminOrg without isAdmin is not
	// SuperAdmin, so the switch is not offered to it and it stays in the admin org.
	if got := value(getBearerHdr(t, app, path, ccAdmin, switchToVictim)); got != adminValue {
		t.Fatalf("admin-org MACHINE token (isAdmin=false) + X-Org-Id:maxpower read %v, want %q "+
			"(no SuperAdmin from owner alone ⇒ no org-switch)", got, adminValue)
	}

	// (b) The DISCRIMINATOR is isMachinePrincipal, not the audience. A REAL admin
	//     (isAdmin=true) gets SuperAdmin whatever app minted the token — audience is not a
	//     gate, admin-org membership + human is the authority — so an admin token with an
	//     arbitrary aud CAN switch into the victim and read it.
	arbAdminTrue := mintRed(t, key, "admin", []string{"some-random-app"}, true, future)
	if got := value(getBearerHdr(t, app, path, arbAdminTrue, switchToVictim)); got != paasValueA {
		t.Fatalf("isAdmin=true + arbitrary aud + org-switch read %v, want %q "+
			"(a real admin is admin from any app)", got, paasValueA)
	}
	//     The ONE exception: a MACHINE principal (its OWN <owner>-platform-kms aud present)
	//     is DENIED SuperAdmin by the machine kind even with isAdmin=true, so the
	//     switch is inert and it stays pinned to owner=admin — a machine identity must
	//     never wield platform-admin.
	machAdminTrue := mintRed(t, key, "admin", []string{"admin-platform-kms"}, true, future)
	if got := value(getBearerHdr(t, app, path, machAdminTrue, switchToVictim)); got != adminValue {
		t.Fatalf("machine principal (isAdmin=true + own machine aud) + org-switch read %v, want %q "+
			"(a machine principal must NEVER receive SuperAdmin)", got, adminValue)
	}
	//     The gate covers ONLY the admin grant: the machine principal still reads its OWN
	//     org, so data-plane access is intact.
	if got := value(getWithBearer(t, app, path, machAdminTrue)); got != adminValue {
		t.Fatalf("admin-org machine principal → own org read %v, want %q (data access intact)", got, adminValue)
	}
}

// ── Vector 3: trim-collapsible / unsafe-rune owner in the SIGNED claim ───────────
//
// owner "maxpower " (trailing space) with a matching machine aud so validate()
// passes. SanitizeIdentity must REFUSE to fold "maxpower " onto tenant "maxpower":
// OrgHasUnsafeRune zeroes the owner → the request is org-less → the guard refuses.
//
// THE REFUSAL CODE IS NOW 400, NOT 403, AND BOTH ARE THE SAME FAIL-CLOSED. An
// org-less request no longer reaches the old `ctx.Org() != :org` comparison (403,
// "you are not that org"); it reaches the edge validator first, which rejects the
// zeroed org as not a DNS-1123 label (400) before any store access. The test
// therefore accepts either refusal and — the part that actually matters and was
// never asserted before — requires the VICTIM'S PLAINTEXT to be absent from the
// response. A collapse onto "maxpower" would show up as a 200 carrying
// paasValueA, which no status assertion alone would have caught.
func TestRed_TrimCollapseOwner_FailsClosed(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("genkey: %v", err)
	}
	jwks := e2eJWKS(t, &key.PublicKey)
	app, deps := newAppWithIdentity(t, e2eCfg(t, jwks.URL))
	sealPlatformSecret(t, deps.KMS, paasOrgA, paasValueA) // maxpower
	path := "/v1/kms" + paasEnvPath
	future := time.Now().Add(time.Hour)

	// refused asserts the one property both codes share: no tenancy was granted, so
	// no tenant's bytes came back.
	refused := func(what string, resp *http.Response) {
		t.Helper()
		body := readAll(resp.Body)
		if strings.Contains(body, paasValueA) {
			t.Fatalf("FOLD BREACH: %s received maxpower's secret: %s", what, body)
		}
		if resp.StatusCode != 400 && resp.StatusCode != 403 {
			t.Fatalf("%s = %d, want 400 or 403 (org-less fail-closed): %s", what, resp.StatusCode, body)
		}
	}

	for _, owner := range []string{"maxpower ", "maxpower​", " maxpower", "maxpower\t"} {
		// aud is bound to the RAW owner so validate()'s machine-aud check passes; the
		// defense must be SanitizeIdentity refusing tenancy for the unsafe owner.
		tok := mintRed(t, key, owner, []string{owner + "-platform-kms"}, false, future)
		refused(fmt.Sprintf("unsafe/trim owner %q", owner), getWithBearer(t, app, path, tok))
		// …and it cannot recover tenancy by SELECTING the victim either: an org-less
		// principal never enters an arm of SanitizeIdentity that honors X-Org-Id.
		refused(fmt.Sprintf("unsafe/trim owner %q + X-Org-Id:maxpower", owner),
			getBearerHdr(t, app, path, tok, map[string]string{"X-Org-Id": paasOrgA}))
	}

	// Empty owner with the bare-suffix aud: the token validates (audience is not a
	// gate) but owner is empty → no org scope → fail closed.
	empty := mintRed(t, key, "", []string{"-platform-kms"}, false, future)
	refused("empty-owner bare-suffix aud", getWithBearer(t, app, path, empty))

	// The victim is untouched throughout — proof the refusals above are the fold
	// being rejected, not the endpoint being broken.
	if got := decode(t, getWithBearer(t, app, path, mintRed(t, key, paasOrgA, []string{"hanzo-console"}, false, future)).Body)["value"]; got != paasValueA {
		t.Fatalf("maxpower read %v, want %q", got, paasValueA)
	}
}

// machineKind returns the kind IAM signs for a client_credentials token when this
// suite's own convention says the subject IS that org's machine, and "" otherwise.
//
// The convention is the owner-bound audience `<owner>-platform-kms`, which the
// probes already use to mean exactly that. It is read HERE, in the minter, and
// nowhere in the code under test: the audience stopped conferring authority when
// the kind became a signed claim, and the point of these probes is that borrowing
// one confers nothing.
func machineKind(owner string, aud []string) string {
	for _, a := range aud {
		if a == owner+"-platform-kms" {
			return authz.Program
		}
	}
	return ""
}
