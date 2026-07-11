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

// OrgMoney returns (spendCents, creditsCents) for one org from commerce. Best-effort:
// unreachable/unconfigured commerce yields zeros.
func OrgMoney(s *cloud.Service[State], ctx context.Context, org string) (int64, int64) {
	var spend, credits int64
	if sp, err := s.State.Commerce.Spend(ctx, org); err == nil {
		spend = int64(sp.Consumed)
	}
	if c, err := s.State.Commerce.Credits(ctx, org); err == nil {
		credits = int64(c)
	}
	return spend, credits
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
