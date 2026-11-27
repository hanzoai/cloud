package referral

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

// maxEdges bounds the edges one call returns.
const maxEdges = 1000

// exposeEdges publishes the referral edges on the internal plane. Use calls it.
func exposeEdges(o referralOps) {
	cloud.Plane().Post("/referral/edges", o.edges,
		zip.WithOperationID(client.ReferralEdges),
		zip.WithSummary("List the referral edges, referrer org to referee org, oldest first"))
}

// edges pages every referral edge for the rewards capability (apps/reward), which
// states a referral's activation and retention to the rules engine from the
// referee org's usage. It answers the two orgs, where the edge stands and when,
// and no code.
//
// It reads across tenants, so only the platform asks: the brand or the admin org.
func (o referralOps) edges(ctx context.Context, in *client.ReferralEdgesIn) (*client.ReferralEdgeList, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok || (org != cloud.Brand() && org != authz.AdminOrg) {
		return nil, zip.ErrForbidden("referral edges: the platform asks")
	}
	if in == nil || in.Offset < 0 || in.Limit < 0 || in.Limit > maxEdges {
		return nil, zip.ErrBadRequest("referral edges: offset and limit must not be negative, limit at most 1000")
	}
	limit := in.Limit
	if limit == 0 {
		limit = maxEdges
	}
	edges, err := o.s.State.store.Edges(ctx, in.Offset, limit)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "referral edges: %v", err)
	}
	return &client.ReferralEdgeList{Edges: edges}, nil
}

// Edges is the referral edges, oldest first, from offset.
func (s *Store) Edges(ctx context.Context, offset, limit int) ([]client.ReferralEdge, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT referrer_org, referee_org, status, created_at, qualified_at FROM referrals ORDER BY created_at ASC, id ASC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []client.ReferralEdge{}
	for rows.Next() {
		var e client.ReferralEdge
		if err := rows.Scan(&e.Referrer, &e.Referee, &e.Status, &e.CreatedAt, &e.QualifiedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
