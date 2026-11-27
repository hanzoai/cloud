package referral

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
)

// TestTheEdgesAreThePlatformsAndPageOldestFirst files three referrals, one of them
// qualified, and reads the edges as the brand and as the admin org, in pages, and as
// a tenant, which is refused.
func TestTheEdgesAreThePlatformsAndPageOldestFirst(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		t.Setenv("CLOUD_BRAND", "hanzo")
		_, s := mountWith(t, newFakeCommerce())
		st := s.State.store
		ctx := context.Background()
		for _, edge := range [][2]string{{"ada", "r1"}, {"ada", "r2"}, {"bob", "r3"}} {
			if _, _, err := st.Claim(ctx, "ref"+edge[1], edge[0], edge[1], "CODE"); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := st.LatchQualified(ctx, "refr2", 1); err != nil {
			t.Fatal(err)
		}
		o := referralOps{s: s}
		for _, org := range []string{"hanzo", "admin"} {
			all, err := o.edges(cloud.For(ctx, org), &client.ReferralEdgesIn{})
			if err != nil || len(all.Edges) != 3 {
				t.Fatalf("as %s: %+v %v", org, all, err)
			}
			byReferee := map[string]client.ReferralEdge{}
			for _, e := range all.Edges {
				byReferee[e.Referee] = e
			}
			if e := byReferee["r2"]; e.Referrer != "ada" || e.Status != StatusQualified || e.QualifiedAt == 0 {
				t.Errorf("r2 = %+v", e)
			}
			if e := byReferee["r3"]; e.Referrer != "bob" || e.Status != StatusSignup {
				t.Errorf("r3 = %+v", e)
			}
		}
		page, err := o.edges(cloud.For(ctx, "hanzo"), &client.ReferralEdgesIn{Offset: 2, Limit: 5})
		if err != nil || len(page.Edges) != 1 {
			t.Errorf("the last page = %+v %v", page, err)
		}
		if _, err := o.edges(cloud.For(ctx, "acme"), &client.ReferralEdgesIn{}); err == nil {
			t.Error("a tenant read the edges")
		}
		if _, err := o.edges(cloud.For(ctx, "hanzo"), &client.ReferralEdgesIn{Limit: maxEdges + 1}); err == nil {
			t.Error("an unbounded page was answered")
		}

	})
}
