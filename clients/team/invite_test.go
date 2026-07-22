package team

import "testing"

// TestOrgGrantRoleCaps proves a workspace invite can never confer an org-level
// IAM admin/owner: owner/admin collapse to member; member and guest pass through.
func TestOrgGrantRoleCaps(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"owner", "member"},
		{"admin", "member"},
		{"member", "member"},
		{roleGuest, roleGuest},
	} {
		if got := orgGrantRole(tc.in); got != tc.want {
			t.Fatalf("orgGrantRole(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
