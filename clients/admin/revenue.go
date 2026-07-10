package admin

// Fleet REVENUE aggregate (/v1/admin/revenue) — the operator's money board: total
// prepaid balances held, total realized spend, MRR, a per-customer revenue table,
// ARPU, and a real spend trend. Global-admin only (s.guard).
//
// This is ORTHOGONAL to /v1/admin/finance: finance is the COGS/margin god-view
// (what WE pay vendors, gross margin, DO-credit runway); revenue is the CUSTOMER
// money view (what each customer holds/spends/subscribes). Both read commerce, but
// answer different questions — one is "are we profitable", the other is "who are
// our paying customers and what do they pay". Every number is a real commerce read;
// an unreachable org degrades to honest zero (never a fabricated figure), and a
// partial fleet read marks its source degraded rather than presenting an undercount
// as authoritative.

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// revenueCustomer is one row of the per-customer revenue table.
type revenueCustomer struct {
	Org          string `json:"org"`
	Display      string `json:"display"`
	Plan         string `json:"plan"`
	BalanceCents int64  `json:"balanceCents"`
	SpendCents   int64  `json:"spendCents"`
	MRRCents     int64  `json:"mrrCents"`
}

// revenueData is the whole GET /v1/admin/revenue payload.
type revenueData struct {
	TotalBalancesCents int64             `json:"totalBalancesCents"`
	TotalSpendCents    int64             `json:"totalSpendCents"`
	MRRCents           int64             `json:"mrrCents"`
	Customers          int               `json:"customers"`
	PayingCustomers    int               `json:"payingCustomers"`
	ARPUCents          int64             `json:"arpuCents"`
	PerCustomer        []revenueCustomer `json:"perCustomer"`
	SpendTrend         []seriesPoint     `json:"spendTrend"`
	GeneratedAt        string            `json:"generatedAt"`
	Sources            []sourceStatus    `json:"sources"`
}

func revenue(s *cloud.Service[state], c *zip.Ctx) error {
	ctx := c.Context()
	cr := callerCreds(c)
	now := time.Now().UTC()

	orgs, err := listOrgs(s, ctx, cr)
	if err != nil {
		return fail(c, err.Error())
	}

	// Per-org money, fanned out concurrently (balance + spend + plan/MRR).
	rows := make([]revenueCustomer, len(orgs))
	oks := make([]bool, len(orgs))
	sem := make(chan struct{}, maxCustomerConcurrency)
	var wg sync.WaitGroup
	for i, o := range orgs {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, o iamOrg) {
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
	acts, ledgerOK := fleetActivity(s, ctx, orgs)
	trend := spendSeries(acts, now.AddDate(0, 0, -30), now, "day")

	// Highest-revenue customers first.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].SpendCents != rows[j].SpendCents {
			return rows[i].SpendCents > rows[j].SpendCents
		}
		return rows[i].BalanceCents > rows[j].BalanceCents
	})

	nowStr := now.Format(time.RFC3339)
	sources := []sourceStatus{srcOf("iam", nil, len(orgs), nowStr)}
	if partial {
		sources = append(sources, srcOf("commerce", errPartialRevenue, len(orgs), nowStr))
	} else {
		sources = append(sources, srcOf("commerce", nil, len(orgs), nowStr))
	}
	if !ledgerOK {
		sources = append(sources, srcOf("commerce-ledger", errPartialRevenue, 0, nowStr))
	}

	return ok(c, revenueData{
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

// revenueOf reads one org's money view (balance + spend + plan/MRR). Returns
// (row, ok): ok is false when the spend OR balance read failed, so the caller can
// mark the fleet total PARTIAL rather than presenting an undercount as complete.
func revenueOf(s *cloud.Service[state], ctx context.Context, o iamOrg) (revenueCustomer, bool) {
	subj := orgSubject(o.Name)
	row := revenueCustomer{Org: o.Name, Display: display(o.DisplayName, o.Name), Plan: "pay-as-you-go"}
	ok := true

	if r, err := s.State.commerce.usageRollup(ctx, o.Name, subj); err == nil {
		row.SpendCents = r.ConsumedCents
	} else {
		ok = false
	}
	if credits, err := s.State.commerce.creditsCents(ctx, o.Name, subj); err == nil {
		row.BalanceCents = credits
	} else {
		ok = false
	}
	if sub, err := s.State.commerce.subscriptionSummary(ctx, o.Name, subj); err == nil {
		row.MRRCents = sub.MRR
		row.Plan = sub.Plan
	}
	return row, ok
}
