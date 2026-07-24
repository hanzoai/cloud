package core

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/iam"
	"github.com/hanzoai/cloud/clients/finance"
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

// OrgMoney returns (spendCents, creditsCents, ok) for one org — the ONE per-org money read
// every fleet aggregator (overview, orgs, revenue) folds over. ok is false ONLY when a read
// FAILED, so a caller folds the per-org failure into a PARTIAL/degraded source rather than
// presenting the resulting undercount as authoritative. An org that simply has no money yet
// reads a clean (0, 0, true), never a failure.
//
// CO-RESIDENCE (the money-plane trap). Commerce's own /v1/billing/* routes are behind
// `//go:build cloud` and are NOT compiled into this binary, so the admin commerce client's
// S2S reads self-dispatch by PATH into cloud's OWN handlers: GET /v1/billing/balance
// re-enters the customer balance handler with no principal (401) and GET
// /v1/billing/usage-rollup is unrouted (404). Reading commerce over HTTP would therefore
// fail for EVERY org and falsely mark the money source DOWN while real money sits in the
// co-resident ledger. So — exactly as clients/billing.balance()/usage() and
// core.grantDeposit already resolve it — prefer the co-resident finance ledger
// (finance.Current()); the commerce S2S read stays only as the split-deploy fallback.
func OrgMoney(s *cloud.Service[State], ctx context.Context, org string) (spend, credits int64, ok bool) {
	if fin := finance.Current(); fin != nil {
		return orgMoneyFromFinance(ctx, fin, org)
	}
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

// orgMoneyFromFinance reads (spend30d, credits, ok) for one org from the co-resident finance
// ledger — the SAME wallet the ai prepaid gate debits and clients/billing shows. credits =
// the org-pool AVAILABLE prepaid balance; spend = the org's metered usage over the trailing
// 30 days (SumUsageSince — the windowed usage source the rolling AI-spend cap already reads,
// matching the SpendCents30d field the overview + orgs surfaces render). ok is false only on
// a REAL read failure, so a no-activity org reads (0, 0, true) and never marks the money
// source degraded.
func orgMoneyFromFinance(ctx context.Context, fin finance.Client, org string) (spend, credits int64, ok bool) {
	ok = true
	if bal, err := fin.Balance(ctx, org, org, "usd", false); err == nil {
		credits = bal.Cents()
	} else {
		ok = false
	}
	if sum, err := fin.SumUsageSince(ctx, org, false, time.Now().AddDate(0, 0, -30).Unix()); err == nil {
		spend = sum
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
