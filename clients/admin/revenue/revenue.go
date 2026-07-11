// Package revenue is the fleet REVENUE aggregate (/v1/admin/revenue) — the operator's
// money board: total prepaid balances held, total realized spend, MRR, a per-customer
// revenue table, ARPU, and a real spend trend. Global-admin only (core.Guard).
//
// This is ORTHOGONAL to /v1/admin/finance: finance is the COGS/margin god-view (what WE
// pay vendors); revenue is the CUSTOMER money view (what each customer holds/spends/
// subscribes). Every number is a real commerce read; an unreachable org degrades to
// honest zero, and a partial fleet read marks its source degraded.
package revenue

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/admin/iam"
	"github.com/zap-proto/zip"
)

// Routes registers the fleet revenue board (global-admin only, cross-tenant profitability).
func Routes(app *zip.App, s *cloud.Service[core.State]) {
	app.Get("/v1/admin/revenue", core.Guard(s, Revenue))
}

// RevenueCustomer is one row of the per-customer revenue table.
type RevenueCustomer struct {
	Org          string `json:"org"`
	Display      string `json:"display"`
	Plan         string `json:"plan"`
	BalanceCents int64  `json:"balanceCents"`
	SpendCents   int64  `json:"spendCents"`
	MRRCents     int64  `json:"mrrCents"`
}

// RevenueData is the whole GET /v1/admin/revenue payload.
type RevenueData struct {
	TotalBalancesCents int64              `json:"totalBalancesCents"`
	TotalSpendCents    int64              `json:"totalSpendCents"`
	MRRCents           int64              `json:"mrrCents"`
	Customers          int                `json:"customers"`
	PayingCustomers    int                `json:"payingCustomers"`
	ARPUCents          int64              `json:"arpuCents"`
	PerCustomer        []RevenueCustomer  `json:"perCustomer"`
	SpendTrend         []core.SeriesPoint `json:"spendTrend"`
	GeneratedAt        string             `json:"generatedAt"`
	Sources            []core.SourceStatus `json:"sources"`
}

// Revenue answers GET /v1/admin/revenue.
func Revenue(s *cloud.Service[core.State], c *zip.Ctx) error {
	ctx := c.Context()
	cr := core.CallerCreds(c)
	now := time.Now().UTC()

	orgs, err := core.ListOrgs(s, ctx, cr)
	if err != nil {
		return core.Fail(c, err.Error())
	}

	// Per-org money, fanned out concurrently (balance + spend + plan/MRR).
	rows := make([]RevenueCustomer, len(orgs))
	oks := make([]bool, len(orgs))
	sem := make(chan struct{}, core.MaxCustomerConcurrency)
	var wg sync.WaitGroup
	for i, o := range orgs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, o iam.Org) {
			defer wg.Done()
			defer func() { <-sem }()
			rows[i], oks[i] = revenueOf(s, ctx, o)
		}(i, o)
	}
	wg.Wait()

	var totalBal, totalSpend, mrr int64
	paying := 0
	partial := false
	for i, r := range rows {
		totalBal += r.BalanceCents
		totalSpend += r.SpendCents
		mrr += r.MRRCents
		if r.SpendCents > 0 || r.MRRCents > 0 {
			paying++
		}
		if !oks[i] {
			partial = true
		}
	}
	arpu := int64(0)
	if paying > 0 {
		arpu = totalSpend / int64(paying)
	}

	// Real 30-day spend trend from the usage ledger (honest empty when no usage).
	acts, ledgerOK := core.FleetActivity(s, ctx, orgs)
	trend := core.SpendSeries(acts, now.AddDate(0, 0, -30), now, "day")

	// Highest-revenue customers first.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].SpendCents != rows[j].SpendCents {
			return rows[i].SpendCents > rows[j].SpendCents
		}
		return rows[i].BalanceCents > rows[j].BalanceCents
	})

	nowStr := now.Format(time.RFC3339)
	sources := []core.SourceStatus{core.SrcOf("iam", nil, len(orgs), nowStr)}
	if partial {
		sources = append(sources, core.SrcOf("commerce", core.ErrPartialRevenue, len(orgs), nowStr))
	} else {
		sources = append(sources, core.SrcOf("commerce", nil, len(orgs), nowStr))
	}
	if !ledgerOK {
		sources = append(sources, core.SrcOf("commerce-ledger", core.ErrPartialRevenue, 0, nowStr))
	}

	return core.OK(c, RevenueData{
		TotalBalancesCents: totalBal,
		TotalSpendCents:    totalSpend,
		MRRCents:           mrr,
		Customers:          len(orgs),
		PayingCustomers:    paying,
		ARPUCents:          arpu,
		PerCustomer:        rows,
		SpendTrend:         trend,
		GeneratedAt:        nowStr,
		Sources:            sources,
	})
}

// revenueOf reads one org's money view (balance + spend + plan/MRR). Returns (row, ok):
// ok is false when the spend OR balance read failed, so the caller can mark the fleet
// total PARTIAL rather than presenting an undercount as complete.
func revenueOf(s *cloud.Service[core.State], ctx context.Context, o iam.Org) (RevenueCustomer, bool) {
	row := RevenueCustomer{Org: o.Name, Display: core.Display(o.DisplayName, o.Name), Plan: "pay-as-you-go"}
	ok := true

	if sp, err := s.State.Commerce.Spend(ctx, o.Name); err == nil {
		row.SpendCents = int64(sp.Consumed)
	} else {
		ok = false
	}
	if credits, err := s.State.Commerce.Credits(ctx, o.Name); err == nil {
		row.BalanceCents = int64(credits)
	} else {
		ok = false
	}
	if pl, err := s.State.Commerce.Plan(ctx, o.Name); err == nil {
		row.MRRCents = int64(pl.MRR)
		row.Plan = pl.Name
	}
	return row, ok
}
