// Copyright 2026 Hanzo AI, Inc. All rights reserved.

package cloud

import (
	"strings"
	"testing"

	"github.com/hanzoai/authz"
)

// TestUsernamePrefersPreferredUsername pins the claim precedence the money path
// depends on. OIDC `name` is a DISPLAY name — IAM fills it from User.DisplayName,
// and a real token carried "Zach Kelling". Reading it as the username addressed
// wallet `hanzo/Zach Kelling`, which no funding path can name, while the balance
// sat in `hanzo/z`; every signed-in completion 402'd against a funded account.
func TestUsernamePrefersPreferredUsername(t *testing.T) {
	// Both present: the username wins, never the human label.
	c := &idClaims{Claims: authz.Claims{Name: "Zach Kelling", PreferredUsername: "z"}}
	if got := c.username(); got != "z" {
		t.Fatalf("username() = %q; want %q (preferred_username must win over the display name)", got, "z")
	}
	// A display name must never be returned when the username is available, and a
	// space is the tell that a display name leaked into an account key.
	if strings.ContainsRune(c.username(), ' ') {
		t.Fatalf("username() = %q; an account key can never contain a space", c.username())
	}
	// Legacy token minted before IAM emitted preferred_username: `name` is all
	// there is, so it stays the answer rather than becoming empty.
	legacy := &idClaims{Claims: authz.Claims{Name: "z"}}
	if got := legacy.username(); got != "z" {
		t.Fatalf("legacy username() = %q; want %q (fallback must be retained)", got, "z")
	}
	// Neither present: empty, never a guess.
	if got := (&idClaims{}).username(); got != "" {
		t.Fatalf("empty username() = %q; want \"\"", got)
	}
}

// TestOrgAdminAdmitsOwner pins the role vocabulary this gate reads. IAM's coarse
// membership set is exactly {owner, admin, member}, and `owner` is what it writes
// for whoever CREATES an org — self-service provisioning calls
// EnsureMembership(..., RoleOwner) precisely so a new org is not "born with nobody
// on it". Matching only "admin" therefore locked every self-serve founder out of
// their own org's admin surface, and an owner cannot escalate their way back in.
//
// The role is FOLDED (a closed vocabulary IAM controls); the org is compared
// VERBATIM, because a fold there would let a member of "acme" claim "ACME".
func TestOrgAdminAdmitsOwner(t *testing.T) {
	for _, tc := range []struct {
		name string
		orgs []authz.Membership
		org  string
		want bool
	}{
		{"owner of the org", []authz.Membership{{Org: "acme", Role: "owner"}}, "acme", true},
		{"admin of the org", []authz.Membership{{Org: "acme", Role: "admin"}}, "acme", true},
		{"role case is folded", []authz.Membership{{Org: "acme", Role: "Owner"}}, "acme", true},
		{"role is trimmed", []authz.Membership{{Org: "acme", Role: " owner "}}, "acme", true},
		{"plain member is not an admin", []authz.Membership{{Org: "acme", Role: "member"}}, "acme", false},
		{"unknown role admits nothing", []authz.Membership{{Org: "acme", Role: "billing"}}, "acme", false},
		{"owner ELSEWHERE does not admit here", []authz.Membership{{Org: "other", Role: "owner"}}, "acme", false},
		{"org compare stays VERBATIM", []authz.Membership{{Org: "ACME", Role: "owner"}}, "acme", false},
		{"empty set admits nothing", nil, "acme", false},
		{"empty org admits nothing", []authz.Membership{{Org: "acme", Role: "owner"}}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isOrgAdmin(tc.orgs, tc.org); got != tc.want {
				t.Fatalf("isOrgAdmin(%v, %q) = %v; want %v", tc.orgs, tc.org, got, tc.want)
			}
		})
	}
}
