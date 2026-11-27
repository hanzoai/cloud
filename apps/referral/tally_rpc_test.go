package referral

import (
	"context"
	"slices"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
)

// TestTheTallyCountsEachOrgForThePlatformOnly files referrals for two orgs, one
// of them qualified, and reads the tally as the brand, as the admin org and as a
// tenant, which is refused.
func TestTheTallyCountsEachOrgForThePlatformOnly(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		t.Setenv("CLOUD_BRAND", "hanzo")
		_, s := mountWith(t, newFakeCommerce())
		st := s.State.store
		ctx := context.Background()
		for i, edge := range [][2]string{{"ada", "r1"}, {"ada", "r2"}, {"bob", "r3"}} {
			if _, _, err := st.Claim(ctx, "ref"+edge[1], edge[0], edge[1], "CODE"); err != nil {
				t.Fatalf("claim %d: %v", i, err)
			}
		}
		if _, err := st.LatchQualified(ctx, "refr2", 1); err != nil {
			t.Fatal(err)
		}
		o := referralOps{s: s}
		for _, org := range []string{"hanzo", "admin"} {
			got, err := o.tally(cloud.For(ctx, org), &client.ReferralTallyIn{Orgs: []string{"ada", "bob", "ada", "cy", " "}})
			if err != nil {
				t.Fatalf("as %s: %v", org, err)
			}
			want := []client.ReferralOrgTally{{Org: "ada", Referrals: 2, Qualified: 1}, {Org: "bob", Referrals: 1}}
			if !slices.Equal(got.Orgs, want) {
				t.Fatalf("as %s: %+v, want %+v", org, got.Orgs, want)
			}
		}
		if _, err := o.tally(cloud.For(ctx, "acme"), &client.ReferralTallyIn{Orgs: []string{"ada"}}); err == nil {
			t.Fatal("a tenant read the tally")
		}
		if _, err := o.tally(cloud.For(ctx, "hanzo"), &client.ReferralTallyIn{Orgs: make([]string, maxTally+1)}); err == nil {
			t.Fatal("an unbounded tally was answered")
		}

	})
}
