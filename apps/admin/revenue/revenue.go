// Package revenue is the fleet REVENUE aggregate (/v1/admin/revenue) — the operator's
// money board: total prepaid balances held, total realized spend, MRR, a per-customer
// revenue table, ARPU, and a real spend trend. SuperAdmin only (core.Admit).
//
// This is ORTHOGONAL to /v1/admin/finance: finance is the COGS/margin god-view (what WE
// pay vendors); revenue is the CUSTOMER money view (what each customer holds/spends/
// subscribes). Every number is a real commerce read; an unreachable org degrades to
// honest zero, and a partial fleet read marks its source degraded.
package revenue

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/admin/iam"
	"github.com/zap-proto/zip"
)

// Routes registers the fleet revenue board (SuperAdmin only, cross-tenant profitability).
func Routes(z *zip.App, s *cloud.Service[core.State]) {
	o := ops{s: s}
	zip.Get(z, "/v1/admin/revenue", o.Revenue, zip.WithOperationID("adminRevenue"))
}

// ops binds the kernel to the typed handler: a TypedHandler has no parameter for the
// service, so it arrives as a RECEIVER and the op is a method value.
type ops struct{ s *cloud.Service[core.State] }

// RevenueOut is the GET /v1/admin/revenue envelope.
type RevenueOut struct {
	// Status is "ok" or "error". Only an unreadable IAM org directory is an error: a
	// per-org money failure degrades that row to zeros and marks the commerce source, so
	// the board still answers.
	Status string `json:"status"`
	// Msg is the failure, and is empty on success.
	Msg string `json:"msg"`
	// Data is the board. Null exactly when Status is "error".
	Data *RevenueData `json:"data"`
}

// RevenueCustomer is one row of the per-customer revenue table.
type RevenueCustomer struct {
	// Org is the tenant slug — the row's identity.
	Org string `json:"org"`
	// Display is the org's display name, falling back to the slug.
	Display string `json:"display"`
	// Plan is the live subscription tier's name, or "pay-as-you-go" — which is also what
	// an org reads as when the subscriptions read failed, so it is not proof of no plan.
	Plan string `json:"plan"`
	// BalanceCents is the prepaid wallet the org still holds, in USD cents. Money not yet
	// earned: it becomes revenue only as it is spent.
	BalanceCents int64 `json:"balanceCents"`
	// SpendCents is realized consumption over the trailing 30 days, in USD cents. The
	// table's primary sort, largest first.
	SpendCents int64 `json:"spendCents"`
	// MRRCents is the org's monthly recurring revenue, in USD cents, over the
	// subscriptions commerce counts as revenue. Contract value, not cash collected.
	MRRCents int64 `json:"mrrCents"`
}

// RevenueData is the whole GET /v1/admin/revenue payload.
type RevenueData struct {
	// TotalBalancesCents is prepaid credit held across the fleet, in USD cents. A
	// LIABILITY, not revenue — it is money customers have not spent yet.
	TotalBalancesCents int64 `json:"totalBalancesCents"`
	// TotalSpendCents is realized consumption across the fleet over the trailing 30 days,
	// in USD cents. This is the revenue figure; balances above are not.
	TotalSpendCents int64 `json:"totalSpendCents"`
	// MRRCents is fleet monthly recurring revenue, in USD cents — the sum of every org's
	// revenue-counting subscriptions. Orthogonal to TotalSpendCents, and never added to
	// it: one is contract value, the other is consumption.
	MRRCents int64 `json:"mrrCents"`
	// Customers is every org IAM lists, paying or not.
	Customers int `json:"customers"`
	// PayingCustomers counts the orgs with spend or MRR above zero. It is the ARPU
	// denominator, chosen so a fleet of free signups cannot deflate the number.
	PayingCustomers int `json:"payingCustomers"`
	// ARPUCents is TotalSpendCents divided by PayingCustomers, in USD cents — average
	// revenue per PAYING customer, not per signup. Zero when nobody pays, which is
	// division declining to happen rather than a measured zero.
	ARPUCents int64 `json:"arpuCents"`
	// PerCustomer is every org, richest first by 30-day spend, then by balance held.
	PerCustomer []RevenueCustomer `json:"perCustomer"`
	// SpendTrend is fleet consumption bucketed by DAY over the last 30 days, each point's
	// value in USD cents. The axis is continuous: a day with no usage is a real 0.
	SpendTrend []core.SeriesPoint `json:"spendTrend"`
	// GeneratedAt is when the read ran, RFC3339. Nothing here is cached.
	GeneratedAt string `json:"generatedAt"`
	// Sources is the freshness strip: "iam" for the org directory, "commerce" for the
	// money, and "commerce-ledger" when the usage history behind SpendTrend was partial.
	// A not-ok commerce row means the totals above are an UNDERCOUNT, not a fleet that
	// earned less.
	Sources []core.SourceStatus `json:"sources"`
}

// Revenue is the fleet money board: total prepaid balances held, total realized spend,
// MRR, ARPU, a per-customer table sorted highest-revenue first, and a real 30-day spend
// trend from the usage ledger.
//
// ORTHOGONAL to /v1/admin/finance, which is the COGS/margin view of what WE pay vendors.
// This is the customer side: what each customer holds, spends and subscribes to.
//
// arpu divides realized spend by PAYING customers, not by all of them — a fleet of free
// signups must not deflate the number. A customer counts as paying when it has spend or
// MRR.
//
// An org whose money did not read degrades to honest zeros and marks the commerce source
// degraded in sources[], so a partial fleet read is visible instead of quietly low.
//
// Response: {"status":"ok","msg":"","data":{"totalBalancesCents":250000,
// "totalSpendCents":180000,"mrrCents":99000,"customers":42,"payingCustomers":11,
// "arpuCents":16363,"perCustomer":[],"spendTrend":[],"generatedAt":"2026-07-27T00:00:00Z",
// "sources":[{"name":"iam","ok":true,"rows":42,"lastSync":"2026-07-27T00:00:00Z"}]}}
func (o ops) Revenue(ctx context.Context, _ *core.None) (*RevenueOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	// Money is read per org, and three of these folds run their orgs in parallel, so
	// the principal is lifted off the request ONCE here and re-pointed per tenant.
	ctx = core.Acting(c)
	data, err := Compute(o.s, ctx, core.CallerCreds(c))
	if err != nil {
		return &RevenueOut{Status: core.Err, Msg: err.Error()}, nil
	}
	return &RevenueOut{Status: core.OK, Data: &data}, nil
}

// Compute builds the fleet revenue aggregate. It is split out of the handler so the
// consolidated money board (/v1/admin/money) folds the SAME numbers this endpoint
// serves: one aggregation, two views, no second implementation to drift.
func Compute(s *cloud.Service[core.State], ctx context.Context, cr iam.Creds) (RevenueData, error) {
	now := time.Now().UTC()

	orgs, err := core.ListOrgs(s, ctx, cr)
	if err != nil {
		return RevenueData{}, err
	}

	// Per-org money, fanned out concurrently (balance + spend + plan/MRR). ONE
	// delegation for the whole fan-out — see core.Delegate.
	money := core.Delegate(ctx)
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
			rows[i], oks[i] = revenueOf(s, ctx, money, o)
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

	return RevenueData{
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
	}, nil
}

// revenueOf reads one org's money view (balance + spend + plan/MRR). Returns (row, ok):
// ok is false when the balance/spend read failed, so the caller can mark the fleet total
// PARTIAL rather than presenting an undercount as complete.
//
// Balance + spend come from the ONE per-org money read (core.OrgMoney): co-resident that
// reads cloud's native finance wallet — the SAME balances the ai gate enforces and the
// overview folds — so the revenue board no longer reads the money source as down when
// commerce is in-process. MRR stays a subscriptions read (a separate endpoint); its
// absence degrades the row to pay-as-you-go and never marks the money source down.
func revenueOf(s *cloud.Service[core.State], ctx context.Context, money core.Delegated, o iam.Org) (RevenueCustomer, bool) {
	row := RevenueCustomer{Org: o.Name, Display: core.Display(o.DisplayName, o.Name), Plan: "pay-as-you-go"}

	spend, balance, err := core.OrgMoney(s, money, o.Name)
	row.SpendCents = spend
	row.BalanceCents = balance
	if pl, err := s.State.Commerce.Plan(ctx, o.Name); err == nil {
		row.MRRCents = int64(pl.MRR)
		row.Plan = pl.Name
	}
	return row, !core.MoneyFailed(err)
}
