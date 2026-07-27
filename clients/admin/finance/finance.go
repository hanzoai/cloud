// Package finance is the SaaS business/finance dashboard (/v1/admin/finance) — the
// profitability panel: what we pay every vendor (COGS), what we earn, the gross margin,
// how fast we're burning the DigitalOcean promo credit, and the runway that credit + burn
// imply. SUPERADMIN ONLY (core.Guard).
//
// It FABRICATES NOTHING and OWNS NO cost logic. COGS is the SINGLE source of truth in
// commerce (GET /v1/costs) — cloud CONSUMES it. Revenue + MRR come from commerce billing.
// The one direct vendor read that remains is the DigitalOcean promo-CREDIT balance +
// burn-down history — an ORTHOGONAL treasury view. The derived margin/runway math is a
// pure function (ComputeFinance) with a unit test proving the numbers.
package finance

import (
	"context"
	"errors"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/commerce"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/admin/iam"
	"github.com/zap-proto/zip"
)

// errUnconfigured marks an upstream that is not wired on this deployment (no DO token /
// no commerce URL). core.SrcOf reports it as a not-ok source so the console shows the
// honest not-configured state rather than a fabricated read.
var errUnconfigured = errors.New("not configured")

// Routes registers the finance dashboard (SuperAdmin only).
func Routes(app cloud.Router, s *cloud.Service[core.State]) {
	g := app.Group("/v1/admin")
	g.Get("/finance", core.Guard(s, Finance))
	// One-time commerce→finance balance cutover (SuperAdmin only). Idempotent per org.
	g.Post("/finance/backfill", core.Guard(s, Backfill))
	// There is no second credit-write here. POST /finance/deposit used to fund an
	// arbitrary subject verbatim, because the credit grant could only reach the org
	// pool; the grant now resolves its address through account.Payer and can name a
	// member, so the raw route's only reason to exist is gone. It was also the unsafe
	// one — no cap, no audit row, and no idempotency ref, so a double-clicked deposit
	// credited twice. ONE credit-write path: core.ApplyGrant.

	// Per-provider upstream credit ledger + usage funding split (multi-provider
	// credit-management). Same SuperAdmin guard, same cloud_usage warehouse.
	g.Get("/providers/credit", core.Guard(s, ProvidersCredit))
	g.Get("/usage/funding", core.Guard(s, UsageFunding))
}

// FinanceData is the full /v1/admin/finance aggregate.
type FinanceData struct {
	Cost        FinanceCost         `json:"cost"`
	Revenue     FinanceRevenue      `json:"revenue"`
	Derived     FinanceDerived      `json:"derived"`
	GeneratedAt string              `json:"generatedAt"`
	Sources     []core.SourceStatus `json:"sources"`
}

// FinanceCost is the platform COGS view — what WE pay our vendors. Its authority is
// commerce GET /v1/costs: TotalCents is the whole-platform COGS the margin math folds,
// and Vendors is the per-vendor breakdown. Configured is false (and every number 0) when
// commerce /v1/costs is unreachable.
//
// DigitalOcean here is an ORTHOGONAL treasury view (promo-credit remaining + burn-down),
// NOT part of COGS: it feeds only the runway projection.
type FinanceCost struct {
	Configured bool              `json:"configured"`
	Error      string            `json:"error,omitempty"`
	Period     string            `json:"period"`
	TotalCents int64             `json:"totalCents"`
	Vendors    []commerce.Vendor `json:"vendors"`

	DigitalOcean DoCost `json:"digitalocean"`
}

// DoCost is the DigitalOcean credit + spend view. When Configured is false every number
// is zero and the console renders the honest "connect DO_API_TOKEN" state.
type DoCost struct {
	Configured            bool             `json:"configured"`
	Error                 string           `json:"error,omitempty"`
	CreditRemainingCents  int64            `json:"creditRemainingCents"`
	MonthToDateSpendCents int64            `json:"monthToDateSpendCents"`
	AvgDailyBurnCents     int64            `json:"avgDailyBurnCents"`
	AccountBalanceCents   int64            `json:"accountBalanceCents"`
	GeneratedAt           string           `json:"generatedAt,omitempty"`
	History               []DoHistoryPoint `json:"history"`
}

// DoHistoryPoint is one credit burn-down series point (usage charge over time).
type DoHistoryPoint struct {
	Date        string `json:"date"`
	AmountCents int64  `json:"amountCents"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

// FinanceRevenue is the commerce revenue view (all money in USD cents).
type FinanceRevenue struct {
	Configured           bool  `json:"configured"`
	TotalRevenueCents    int64 `json:"totalRevenueCents"`
	MRRCents             int64 `json:"mrrCents"`
	CreditsConsumedCents int64 `json:"creditsConsumedCents"`
}

// FinanceDerived is the pure profitability math. Runway is a pointer so it can be null
// (no honest runway when burn is zero or DO is unconfigured).
type FinanceDerived struct {
	GrossMarginCents int64    `json:"grossMarginCents"`
	GrossMarginPct   float64  `json:"grossMarginPct"`
	RunwayDays       *float64 `json:"runwayDays"`
	Profitable       bool     `json:"profitable"`
}

// FinanceInput is the raw material ComputeFinance folds into FinanceData. The handler
// fills Cost from the commerce COGS read (+ the DO-credit treasury view) and Revenue from
// commerce billing; the pure function does the math so the derivation is unit-testable in
// isolation.
type FinanceInput struct {
	Cost        FinanceCost
	Revenue     FinanceRevenue
	GeneratedAt string
	Sources     []core.SourceStatus
}

// ComputeFinance is the PURE derivation: given the multi-vendor COGS view and the commerce
// revenue view, it computes gross margin, margin %, runway, and profitability. No I/O.
//
//	grossMarginCents = revenue - COGS(total, all vendors)
//	grossMarginPct   = grossMargin / revenue * 100          (0 when revenue is 0)
//	runwayDays       = DO creditRemaining / DO avgDailyBurn  (nil when burn 0 or DO off)
//	profitable       = revenue > COGS
func ComputeFinance(in FinanceInput) FinanceData {
	cost := in.Cost.TotalCents
	rev := in.Revenue.TotalRevenueCents

	margin := rev - cost
	var marginPct float64
	if rev > 0 {
		marginPct = (float64(margin) / float64(rev)) * 100
	}

	// Runway is the DO promo-credit treasury projection (orthogonal to COGS). Nil when DO
	// is off or burn is 0 — never a fabricated infinity.
	do := in.Cost.DigitalOcean
	var runway *float64
	if do.Configured && do.AvgDailyBurnCents > 0 {
		d := float64(do.CreditRemainingCents) / float64(do.AvgDailyBurnCents)
		runway = &d
	}

	return FinanceData{
		Cost:    in.Cost,
		Revenue: in.Revenue,
		Derived: FinanceDerived{
			GrossMarginCents: margin,
			GrossMarginPct:   marginPct,
			RunwayDays:       runway,
			Profitable:       rev > cost,
		},
		GeneratedAt: in.GeneratedAt,
		Sources:     in.Sources,
	}
}

// Finance answers GET /v1/admin/finance. It reads the multi-vendor COGS from commerce
// /v1/costs, the DO promo-credit/burn-down treasury view, and the fleet commerce revenue,
// then hands them to ComputeFinance. SuperAdmin only.
func Finance(s *cloud.Service[core.State], c *zip.Ctx) error {
	return core.OK(c, Compute(s, c.Context(), core.CallerCreds(c)))
}

// Compute gathers the cost/revenue inputs and folds them through ComputeFinance. It is
// split out of the handler so the consolidated money board (/v1/admin/money) reports
// the SAME infrastructure cost and margin this endpoint serves — one aggregation, two
// views. (ComputeFinance stays the PURE fold; this is the I/O half in front of it.)
func Compute(s *cloud.Service[core.State], ctx context.Context, cr iam.Creds) FinanceData {
	now := time.Now().UTC().Format(time.RFC3339)
	period := time.Now().UTC().Format("2006-01")

	var sources []core.SourceStatus

	// ── COGS: commerce /v1/costs (the single vendor-COGS source of truth) ──
	cost := FinanceCost{Period: period}
	if s.State.Commerce.Ready() {
		report, err := s.State.Commerce.Costs(ctx, period)
		if err != nil {
			cost.Error = err.Error()
			sources = append(sources, core.SrcOf("commerce-costs", err, 0, now))
		} else {
			cost.Configured = true
			cost.TotalCents = int64(report.Total)
			cost.Vendors = report.Vendors
			if report.Period != "" {
				cost.Period = report.Period
			}
			sources = append(sources, core.SrcOf("commerce-costs", nil, len(report.Vendors), now))
		}
	} else {
		cost.Error = "commerce /v1/costs not configured"
		sources = append(sources, core.SrcOf("commerce-costs", errUnconfigured, 0, now))
	}
	if cost.Vendors == nil {
		cost.Vendors = []commerce.Vendor{}
	}

	// ── DigitalOcean promo-credit / runway (orthogonal treasury view) ──
	do := DoCost{Configured: s.State.DO.Ready()}
	if !s.State.DO.Ready() {
		do.Error = "DO_API_TOKEN not configured"
		sources = append(sources, core.SrcOf("digitalocean", errUnconfigured, 0, now))
	} else {
		bal, err := s.State.DO.Balance(ctx)
		if err != nil {
			do.Error = err.Error()
			sources = append(sources, core.SrcOf("digitalocean", err, 0, now))
		} else {
			// creditRemaining = -account_balance clamped at 0 (negative account balance =
			// credit we hold; a positive balance means we owe DO → 0 credit).
			credit := -int64(bal.Account)
			if credit < 0 {
				credit = 0
			}
			do.CreditRemainingCents = credit
			do.MonthToDateSpendCents = int64(bal.Usage)
			do.AccountBalanceCents = int64(bal.Account)
			do.GeneratedAt = bal.At
			do.AvgDailyBurnCents = AvgDailyBurnCents(int64(bal.Usage), time.Now().UTC())
			do.History = doHistory(s, ctx)
			sources = append(sources, core.SrcOf("digitalocean", nil, 1, now))
		}
	}
	if do.History == nil {
		do.History = []DoHistoryPoint{}
	}
	cost.DigitalOcean = do

	// ── Revenue: commerce (fleet-wide) ────────────────────────────────────
	rev := FinanceRevenue{}
	if !s.State.Commerce.Ready() {
		sources = append(sources, core.SrcOf("commerce", errUnconfigured, 0, now))
	} else if orgs, orgErr := core.ListOrgs(s, ctx, cr); orgErr != nil {
		// The revenue source is unreadable → honest not-configured, never a zero that
		// would flip the margin negative on an upstream hiccup.
		sources = append(sources, core.SrcOf("commerce", orgErr, 0, now))
	} else {
		var totalRev, mrr int64
		partial := false
		for _, o := range orgs {
			if sp, e := s.State.Commerce.Spend(ctx, o.Name); e == nil {
				totalRev += int64(sp.Consumed)
			} else {
				partial = true
			}
			if pl, e := s.State.Commerce.Plan(ctx, o.Name); e == nil {
				mrr += int64(pl.MRR)
			} else {
				partial = true
			}
		}
		rev.Configured = true
		rev.TotalRevenueCents = totalRev
		rev.CreditsConsumedCents = totalRev
		rev.MRRCents = mrr
		if partial {
			sources = append(sources, core.SrcOf("commerce", core.ErrPartialRevenue, len(orgs), now))
		} else {
			sources = append(sources, core.SrcOf("commerce", nil, len(orgs), now))
		}
	}

	return ComputeFinance(FinanceInput{
		Cost:        cost,
		Revenue:     rev,
		GeneratedAt: now,
		Sources:     sources,
	})
}

// doHistory reads DO billing history into the burn-down series (best-effort: a failure
// yields an empty series, never a fabricated trend).
func doHistory(s *cloud.Service[core.State], ctx context.Context) []DoHistoryPoint {
	entries, err := s.State.DO.History(ctx, 60)
	if err != nil {
		return []DoHistoryPoint{}
	}
	pts := make([]DoHistoryPoint, 0, len(entries))
	for _, e := range entries {
		pts = append(pts, DoHistoryPoint{
			Date:        e.Date,
			AmountCents: int64(e.Amount),
			Type:        e.Kind,
			Description: e.Description,
		})
	}
	return pts
}

// AvgDailyBurnCents derives the average daily DO burn from month-to-date usage:
// month-to-date spend divided by the number of elapsed days in the current month (at
// least 1, so day 1 doesn't divide by zero).
func AvgDailyBurnCents(monthToDateSpendCents int64, now time.Time) int64 {
	day := now.Day()
	if day < 1 {
		day = 1
	}
	return monthToDateSpendCents / int64(day)
}
