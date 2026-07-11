package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/iam"
)

// MaxCustomerConcurrency bounds the per-org enrichment fan-out so a large fleet does
// not open one upstream connection per org at once. Admin is low-QPS; 8 keeps latency
// low without hammering IAM/commerce.
const MaxCustomerConcurrency = 8

// ListOrgs reads the org directory (owner = admin org) as the typed shape the
// overview/orgs/usage/customer/revenue/finance aggregators fold over.
func ListOrgs(s *cloud.Service[State], ctx context.Context, cr iam.Creds) ([]iam.Org, error) {
	q := url.Values{}
	q.Set("owner", s.State.AdminOrg)
	res, err := s.State.IAM.Orgs(ctx, cr, q)
	if err != nil {
		return nil, err
	}
	var orgs []iam.Org
	if len(res.Rows) > 0 {
		if err := json.Unmarshal(res.Rows, &orgs); err != nil {
			return nil, fmt.Errorf("orgs decode: %w", err)
		}
	}
	return orgs, nil
}

// OrgMoney returns (spendCents, creditsCents, ok) for one org from commerce. ok is
// false when the spend OR credits read FAILED — so a fleet aggregator can fold the
// per-org failure into a PARTIAL/degraded source rather than presenting the resulting
// undercount as authoritative (the SAME (row, ok) contract revenue.revenueOf uses). An
// unwired commerce is NOT a failure: Spend/Credits return (0, nil) when unconfigured, so
// ok stays true and the caller distinguishes "not configured" via Commerce.Ready().
func OrgMoney(s *cloud.Service[State], ctx context.Context, org string) (spend, credits int64, ok bool) {
	ok = true
	if sp, err := s.State.Commerce.Spend(ctx, org); err == nil {
		spend = int64(sp.Consumed)
	} else {
		ok = false
	}
	if c, err := s.State.Commerce.Credits(ctx, org); err == nil {
		credits = int64(c)
	} else {
		ok = false
	}
	return spend, credits, ok
}

// FindOrg returns the IAM org by slug (nil, nil when it does not exist) so a management
// action can validate its target before acting — never credit or suspend an org that
// isn't real.
func FindOrg(s *cloud.Service[State], ctx context.Context, cr iam.Creds, org string) (*iam.Org, error) {
	orgs, err := ListOrgs(s, ctx, cr)
	if err != nil {
		return nil, err
	}
	for i := range orgs {
		if orgs[i].Name == org {
			return &orgs[i], nil
		}
	}
	return nil, nil
}

// Display returns displayName when non-blank, else the fallback.
func Display(displayName, fallback string) string {
	if strings.TrimSpace(displayName) != "" {
		return displayName
	}
	return fallback
}
