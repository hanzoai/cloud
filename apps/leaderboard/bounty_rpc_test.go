package leaderboard

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
)

// TestBountyTotalsAreThePlatformsAndRefuseATenant reads the totals as the brand, as
// the admin org, and as a tenant, which is refused.
func TestBountyTotalsAreThePlatformsAndRefuseATenant(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		t.Setenv("CLOUD_BRAND", "hanzo")
		mountApp(t)
		ctx := context.Background()
		if err := mountedStore.PutBountyAward(ctx, bountyAward{ID: "b1", Org: "acme", Kind: "bounty", Points: 70, CreatedAt: 1}); err != nil {
			t.Fatal(err)
		}
		o := boardOps{s: &cloud.Service[state]{State: state{store: mountedStore}}}
		for _, org := range []string{"hanzo", "admin"} {
			out, err := o.bountyTotals(cloud.For(ctx, org), nil)
			if err != nil || out.Map()["acme"] != 70 {
				t.Errorf("as %s: %+v %v", org, out, err)
			}
		}
		if _, err := o.bountyTotals(cloud.For(ctx, "acme"), nil); err == nil {
			t.Error("a tenant read the bounty totals")
		}
	})
}
