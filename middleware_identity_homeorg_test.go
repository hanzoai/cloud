package cloud

// The home org must be the USER's, never the minting APPLICATION's.
//
// IAM stamps the app's organization into the `owner` claim (oidc/jwt.go Sign:
// `Owner: app.Organization`). Cloud read that claim as the user's tenant, so the
// same person authenticating through two different apps resolved two different
// orgs — and since ONE value fed both the billing anchor and the SuperAdmin
// predicate, that made the tenant a caller-selectable choice and the platform-admin
// gate a property of the app you logged in through.
//
// Every existing test mints through a SINGLE app, so `owner` and the user's org were
// always equal in the fixtures and no assertion could tell them apart. These pin the
// difference: each token below has an app org that DISAGREES with the user's home
// org, which is exactly the shape production produces and the suite never did.

import (
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	model "github.com/hanzoai/iam/pkg/model"
	"github.com/zap-proto/zip"
)

// crossAppClaims builds a token the way IAM really mints one: `owner` (and the
// audience) belong to the APP, while the signed `orgs` set is the USER's own
// tenancy, home first. appOrg and userHome are deliberately allowed to differ —
// that divergence is the entire subject of this file.
func crossAppClaims(appOrg, aud, userHome string, isAdmin bool, memberOf ...string) idClaims {
	c := tokenClaims(aud, appOrg, "alice@example.test", isAdmin, time.Now().Add(time.Hour))
	c.Name = "alice"
	c.Subject = "u-alice" // ONE person, whichever app mints the token
	c.Orgs = []model.OrgRef{{Org: userHome, Role: "member"}}
	for _, o := range memberOf {
		c.Orgs = append(c.Orgs, model.OrgRef{Org: o, Role: "member"})
	}
	return c
}

// orgFor runs a token through the boundary and reports the resolved effective org
// and whether platform admin was granted.
func orgFor(t *testing.T, claims idClaims, selected string) (org string, admin bool) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa key: %v", err)
	}
	jwks := jwksServer(t, &key.PublicKey)
	v := newIdentityValidator(testIssuer, jwks.URL, 0)
	tok := signWith(t, key, claims)

	app := zip.New(zip.Config{})
	app.Use(SanitizeIdentity(v, "admin"))
	app.Get("/probe", func(c *zip.Ctx) error {
		org, admin = c.Org(), c.IsAdmin()
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	if selected != "" {
		req.Header.Set("X-Org-Id", selected)
	}
	if _, err := app.Fiber().Test(req); err != nil {
		t.Fatalf("probe: %v", err)
	}
	return org, admin
}

// TestHomeOrgIsAppIndependent is THE bug in one assertion: one person, two apps in
// DIFFERENT orgs, one anchor. Before the fix the anchor followed whichever app
// minted the token, so the user chose their own tenant by choosing a login route.
func TestHomeOrgIsAppIndependent(t *testing.T) {
	viaHanzo, adminH := orgFor(t, crossAppClaims("hanzo", "hanzo-cloud", "gotham-labs", false), "")
	viaLux, adminL := orgFor(t, crossAppClaims("lux", "lux-cloud", "gotham-labs", false), "")

	if viaHanzo != "gotham-labs" || viaLux != "gotham-labs" {
		t.Fatalf("anchor followed the APP: via hanzo-cloud=%q via lux-cloud=%q, want %q both",
			viaHanzo, viaLux, "gotham-labs")
	}
	if viaHanzo != viaLux {
		t.Fatalf("same user resolved two tenants: %q vs %q", viaHanzo, viaLux)
	}
	if adminH || adminL {
		t.Fatalf("a plain member was granted admin (hanzo=%v lux=%v)", adminH, adminL)
	}
}

// TestCrossBrandAppBillsTheUsersOrg is the tenancy half of the same bug. lux-cloud,
// zoo-cloud and pars-cloud are all live accepted audiences, so a hanzo user could
// authenticate through a sibling BRAND's app and spend that brand's ledger. This
// asserts the money, not just the header: billOrg/billUser are what the gate checks
// and the meter debits.
func TestCrossBrandAppBillsTheUsersOrg(t *testing.T) {
	billOrg, billUser, dataOrg, ok := walletProbe(t, crossAppClaims("lux", "lux-cloud", "hanzo", false), "")
	if !ok {
		t.Fatalf("no wallet resolved for a valid principal")
	}
	if billOrg != "hanzo" {
		t.Fatalf("a hanzo user via a lux app billed %q, want %q — cross-brand tenancy breach", billOrg, "hanzo")
	}
	if billUser == "lux" || dataOrg == "lux" {
		t.Fatalf("lux leaked into the wallet: billUser=%q dataOrg=%q", billUser, dataOrg)
	}
}

// TestAppOrgAdminDoesNotGrantSuperAdmin is the privilege-escalation guard. An app
// owned by the reserved admin org (admin-console, hanzo-admin-guard — both live,
// both with the tenant gate orgChoiceMode=None) must not confer platform admin on
// an ordinary user who happens to authenticate through it.
//
// Reading the USER's home org is the WHOLE guard here, and deliberately so: the
// isAdmin bit is not a second term (admin-org membership IS the predicate — see
// TestSuperAdminGate_IsAdminOrgMembership). The two `isAdmin` values below therefore
// prove the guard holds on the org alone, whichever way that bit falls.
func TestAppOrgAdminDoesNotGrantSuperAdmin(t *testing.T) {
	for _, isAdmin := range []bool{false, true} {
		org, admin := orgFor(t, crossAppClaims("admin", "admin-console", "gotham-labs", isAdmin), "")
		if admin {
			t.Fatalf("isAdmin=%v: an ordinary user became SuperAdmin by authenticating through an admin-org app", isAdmin)
		}
		if org != "gotham-labs" {
			t.Fatalf("isAdmin=%v: effective org = %q, want %q (the USER's org, not the app's)", isAdmin, org, "gotham-labs")
		}
	}
}

// TestRealAdminOrgMemberStillSuperAdmin is the lockout guard, and the reason the
// isAdmin bit was NOT added as a second term: a genuine operator whose HOME org is
// the admin org must keep platform sudo, including when their user row carries no
// isAdmin bit. The admin org holds only SuperAdmins (provisioned in, never
// promoted), so membership is the fact. Breaking this locks every operator out of
// admin.hanzo.ai.
func TestRealAdminOrgMemberStillSuperAdmin(t *testing.T) {
	for _, isAdmin := range []bool{false, true} {
		// App org is `hanzo` while the USER's home org is `admin` — sudo must follow
		// the person, not the login route.
		org, admin := orgFor(t, crossAppClaims("hanzo", "hanzo-cloud", "admin", isAdmin), "")
		if !admin {
			t.Fatalf("isAdmin=%v: a real admin-org member was DENIED SuperAdmin — operator lockout", isAdmin)
		}
		if org != "admin" {
			t.Fatalf("isAdmin=%v: effective org = %q, want %q", isAdmin, org, "admin")
		}
	}
}

// TestMasqueradeStillBillsAdminLedger: a real SuperAdmin acting in a customer org
// still spends their OWN books. The carve-out must survive both changes.
func TestMasqueradeStillBillsAdminLedger(t *testing.T) {
	billOrg, _, dataOrg, ok := walletProbe(t, crossAppClaims("hanzo", "hanzo-cloud", "admin", true), "victim")
	if !ok {
		t.Fatalf("no wallet resolved for the admin principal")
	}
	if billOrg != "admin" {
		t.Fatalf("masquerade billed %q, want %q — an admin must never spend the customer's money", billOrg, "admin")
	}
	if dataOrg != "victim" {
		t.Fatalf("data scope = %q, want %q (DATA follows the masquerade, money does not)", dataOrg, "victim")
	}
}

// TestNonMemberSwitchPinsToResolvedHome: the membership gate must measure a
// requested switch against the USER's set and fall back to the USER's home — not to
// the app's org, which is what the fallback used to be.
func TestNonMemberSwitchPinsToResolvedHome(t *testing.T) {
	org, _ := orgFor(t, crossAppClaims("lux", "lux-cloud", "gotham-labs", false, "acme"), "victim")
	if org != "gotham-labs" {
		t.Fatalf("non-member switch resolved %q, want home %q (and never the app org %q)", org, "gotham-labs", "lux")
	}
	// The control: a switch the signed set DOES contain is still honored.
	if org, _ := orgFor(t, crossAppClaims("lux", "lux-cloud", "gotham-labs", false, "acme"), "acme"); org != "acme" {
		t.Fatalf("member switch resolved %q, want %q", org, "acme")
	}
}

// TestLegacyOrgsClaimFailsClosed: a token with no `orgs` (minted before IAM
// v1.33.0) resolves NO org rather than falling back to the app's org. Denied is
// recoverable by re-auth within a token TTL; silently billing the app's tenant is
// not.
func TestLegacyOrgsClaimFailsClosed(t *testing.T) {
	legacy := tokenClaims("hanzo-cloud", "hanzo", "old@example.test", false, time.Now().Add(time.Hour))
	legacy.Orgs = nil // pre-claim token

	org, admin := orgFor(t, legacy, "")
	if org != "" {
		t.Fatalf("legacy token resolved org %q, want \"\" — it must never fall back to the app org", org)
	}
	if admin {
		t.Fatalf("legacy token granted admin")
	}

	// And it must not be rescuable by naming an org on the request either.
	if org, _ := orgFor(t, legacy, "hanzo"); org != "" {
		t.Fatalf("legacy token + requested org resolved %q, want \"\"", org)
	}
}

// TestLegacyAdminOrgTokenIsNotSuperAdmin is the escalation twin of the test above:
// the fail-closed path must not hand admin to a claim-less token minted by an
// admin-org app, which is precisely the combination the old code granted sudo to.
func TestLegacyAdminOrgTokenIsNotSuperAdmin(t *testing.T) {
	legacy := tokenClaims("admin-console", "admin", "old@example.test", true, time.Now().Add(time.Hour))
	legacy.Orgs = nil

	if org, admin := orgFor(t, legacy, ""); admin || org != "" {
		t.Fatalf("legacy admin-org token: org=%q admin=%v, want \"\" and false", org, admin)
	}
}
