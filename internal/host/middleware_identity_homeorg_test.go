package host

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
	"testing"
	"time"

	"github.com/hanzoai/authz"
)

// crossAppClaims builds a token the way IAM really mints one: `owner` (and the
// audience) belong to the APP, while the signed `orgs` set is the USER's own
// tenancy, home first. appOrg and userHome are deliberately allowed to differ —
// that divergence is the entire subject of this file.
func crossAppClaims(appOrg, aud, userHome string, isAdmin bool, memberOf ...string) idClaims {
	c := tokenClaims(aud, appOrg, "alice@example.test", isAdmin, time.Now().Add(time.Hour))
	c.Name = "alice"
	c.Subject = "u-alice" // ONE person, whichever app mints the token
	c.Orgs = []authz.Membership{{Org: userHome, Role: "member"}}
	for _, o := range memberOf {
		c.Orgs = append(c.Orgs, authz.Membership{Org: o, Role: "member"})
	}
	return c
}

// TestCrossBrandAppBillsTheUsersOrg is the tenancy half of the same bug. lux-cloud,
// zoo-cloud and pars-cloud are all live accepted audiences, so a hanzo user could
// authenticate through a sibling BRAND's app and spend that brand's ledger. This
// asserts the money, not just the header: billOrg/billUser are what the gate checks
// and the meter debits.
func TestCrossBrandAppBillsTheUsersOrg(t *testing.T) {
	billOrg, billUser, dataOrg, ok := walletProbe(t, crossAppClaims("lux", "lux-cloud", "gotham-labs", false), "")
	if !ok {
		t.Fatalf("no wallet resolved for a valid principal")
	}
	if billOrg != "gotham-labs" {
		t.Fatalf("a gotham-labs user via a lux app billed %q, want %q — cross-brand tenancy breach", billOrg, "gotham-labs")
	}
	if billUser == "lux" || dataOrg == "lux" {
		t.Fatalf("lux leaked into the wallet: billUser=%q dataOrg=%q", billUser, dataOrg)
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
