package leaderboard

import (
	"context"
	"net/http"

	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

// exposeBountyTotals publishes each org's bounty points on the internal plane.
// routes calls it.
func exposeBountyTotals(o boardOps) {
	cloud.Plane().Post("/leaderboard/bounty/totals", o.bountyTotals,
		zip.WithOperationID(client.LeaderboardBountyTotals),
		zip.WithSummary("Answer the points of the bounty awards each org holds"))
}

// bountyTotals answers the bounty points per org: the same sums the standing counts
// (BountyTotals), for the rewards program's one-time backfill (apps/reward), which
// credits an org's awards to the person who owns the org. It reads across tenants,
// so only the platform asks: the brand or the admin org.
func (o boardOps) bountyTotals(ctx context.Context, _ *cloud.Unit) (*client.BountyTotals, error) {
	if org, ok := cloud.Tenant(ctx); !ok || (org != cloud.Brand() && org != authz.AdminOrg) {
		return nil, zip.ErrForbidden("bounty totals: the platform asks")
	}
	totals, err := o.s.State.store.BountyTotals(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "bounty totals: %v", err)
	}
	return &client.BountyTotals{Orgs: client.OrgTotals(totals)}, nil
}
