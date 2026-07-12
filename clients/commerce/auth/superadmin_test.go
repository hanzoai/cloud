package auth

import "testing"

// TestIAMClaims_IsSuperAdmin pins the canonical platform-sudo predicate shared by
// every cross-org gate (edge billing ?org override, checkout tenant admin, the
// money-mint gate). ONE signal: the HOME org is the reserved "admin" org. Two
// decisive properties:
//   - an org owner (org-level IsAdmin) is NOT a SuperAdmin (no escalation); and
//   - the HOME org (homeOrg) decides — NEVER the effective X-Org-Id — so a
//     SuperAdmin org-switch keeps sudo, and a switched-into "admin" X-Org-Id can
//     never grant sudo to a non-admin-home caller. No boolean claim is read.
func TestIAMClaims_IsSuperAdmin(t *testing.T) {
	cases := []struct {
		name   string
		claims *IAMClaims
		want   bool
	}{
		{"nil", nil, false},
		// JWT-validated path (EdgeAuth): Owner IS the `owner` claim (== home).
		{"admin org (owner=home)", &IAMClaims{Owner: "admin"}, true},
		{"admin org mixed-case", &IAMClaims{Owner: "Admin"}, true},
		{"admin org padded", &IAMClaims{Owner: " admin "}, true},
		// Header-sourced path (GetIAMClaims): HomeOrg = X-User-Owner (home),
		// Owner = X-Org-Id (effective). HomeOrg decides.
		{"masquerade: home=admin, effective=victim → SuperAdmin", &IAMClaims{HomeOrg: "admin", Owner: "victim"}, true},
		{"home=admin, no effective", &IAMClaims{HomeOrg: "admin"}, true},
		// THE anti-escalation: a non-admin HOME can never become SuperAdmin, even if
		// the effective X-Org-Id (Owner) is "admin" — home wins, effective can't grant.
		{"effective=admin but home=hanzo → NOT SuperAdmin", &IAMClaims{HomeOrg: "hanzo", Owner: "admin"}, false},
		// Org owner (org-level IsAdmin) is NOT a SuperAdmin.
		{"org-owner isAdmin is NOT SuperAdmin (maxpower)", &IAMClaims{Owner: "maxpower", IsAdmin: true}, false},
		{"platform org is NOT the admin org", &IAMClaims{Owner: "platform", IsAdmin: true}, false},
		{"plain user", &IAMClaims{Owner: "hanzo"}, false},
		{"empty owner", &IAMClaims{Owner: ""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.claims.IsSuperAdmin(); got != tc.want {
				t.Fatalf("%s: IsSuperAdmin()=%v want %v", tc.name, got, tc.want)
			}
		})
	}
}
