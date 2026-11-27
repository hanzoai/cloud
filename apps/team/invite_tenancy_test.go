package team

import (
	"testing"

	model "github.com/hanzoai/iam/pkg/model"
)

// The refresh offers the orgs a person acts in and no other: a directory and a
// brand org held as a plain member are left out, as the session set leaves them.
func TestTheMembershipRefreshOffersOnlyTenancy(t *testing.T) {
	got := tenancy([]model.OrgRef{
		{Org: "hanzo-id", Role: "member"}, {Org: "ann", Role: "owner"}, {Org: "hanzo", Role: "member"},
		{Org: "lux", Role: "admin"}, {Org: "acme", Role: "member"},
	})
	var orgs []string
	for _, r := range got {
		orgs = append(orgs, r.Org)
	}
	if len(orgs) != 3 || orgs[0] != "ann" || orgs[1] != "lux" || orgs[2] != "acme" {
		t.Fatalf("refresh offered %v, want ann, lux, acme", orgs)
	}
}
