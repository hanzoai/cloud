package auth

import "testing"

// TestIAMClaims_GlobalAdmin pins the canonical platform-admin predicate shared
// by every cross-org gate (edge billing ?org override, checkout tenant admin).
// The decisive case: an org owner (org-level IsAdmin) is NOT a global admin.
func TestIAMClaims_GlobalAdmin(t *testing.T) {
	cases := []struct {
		name   string
		claims *IAMClaims
		want   bool
	}{
		{"nil", nil, false},
		{"admin org", &IAMClaims{Owner: "admin"}, true},
		{"admin org mixed-case", &IAMClaims{Owner: "Admin"}, true},
		{"admin org padded", &IAMClaims{Owner: " admin "}, true},
		{"explicit global flag", &IAMClaims{Owner: "hanzo", IsGlobalAdmin: true}, true},
		{"org-owner isAdmin is NOT global (maxpower)", &IAMClaims{Owner: "maxpower", IsAdmin: true}, false},
		{"platform org is NOT the admin org", &IAMClaims{Owner: "platform", IsAdmin: true}, false},
		{"plain user", &IAMClaims{Owner: "hanzo"}, false},
		{"empty owner", &IAMClaims{Owner: ""}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.claims.GlobalAdmin(); got != tc.want {
				t.Fatalf("%s: GlobalAdmin()=%v want %v", tc.name, got, tc.want)
			}
		})
	}
}
