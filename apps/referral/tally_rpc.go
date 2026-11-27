package referral

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

// maxTally bounds the orgs one tally names.
const maxTally = 1000

// exposeTally publishes the referral tally on the internal plane. Use calls it.
func exposeTally(o referralOps) {
	cloud.Plane().Post("/referral/tallies", o.tally,
		zip.WithOperationID(client.ReferralTallies),
		zip.WithSummary("Count the referrals of each org named, and how many qualified"))
}

// exposeSeries publishes the moments referred orgs signed up. Use calls it.
func exposeSeries(o referralOps) {
	cloud.Plane().Post("/referral/series", o.series,
		zip.WithOperationID(client.ReferralSeries),
		zip.WithSummary("List when each referred org signed up, without who"))
}

// series lists the moments referred orgs signed up since in.Since, for the public
// pulse. Like tally it reads across tenants, so only the platform asks, and it
// answers times alone.
func (o referralOps) series(ctx context.Context, in *client.ReferralSeriesIn) (*client.ReferralSignups, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok || (org != cloud.Brand() && org != authz.AdminOrg) {
		return nil, zip.ErrForbidden("referral series: the platform asks")
	}
	at, err := o.s.State.store.Signups(ctx, in.Since)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "referral series: %v", err)
	}
	return &client.ReferralSignups{At: at}, nil
}

// tally counts each named org's referrals for the beta leaderboard
// (apps/leaderboard beta.go), which ranks the waitlist by them. It answers counts
// and nothing else: no referee, no code, no time.
//
// It reads across tenants, so only the platform asks it: the brand whose
// waitlist it is, or the admin org. A tenant's own count is GET /v1/referral.
func (o referralOps) tally(ctx context.Context, in *client.ReferralTallyIn) (*client.ReferralTally, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok || (org != cloud.Brand() && org != authz.AdminOrg) {
		return nil, zip.ErrForbidden("referral tally: the platform asks")
	}
	if len(in.Orgs) > maxTally {
		return nil, zip.ErrBadRequest("referral tally: too many orgs")
	}
	orgs, seen := make([]string, 0, len(in.Orgs)), map[string]bool{}
	for _, name := range in.Orgs {
		if name = strings.TrimSpace(name); name != "" && !seen[name] {
			seen[name] = true
			orgs = append(orgs, name)
		}
	}
	counts, err := o.s.State.store.Tally(ctx, orgs)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "referral tally: %v", err)
	}
	out := &client.ReferralTally{Orgs: []client.ReferralOrgTally{}}
	for _, org := range orgs {
		if c, ok := counts[org]; ok {
			out.Orgs = append(out.Orgs, client.ReferralOrgTally{Org: org, Referrals: c.Total, Qualified: c.Qualified})
		}
	}
	return out, nil
}
