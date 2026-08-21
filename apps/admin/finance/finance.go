// Package finance is the SaaS business/finance dashboard (/v1/admin/finance) — the
// profitability panel: what we pay every vendor (COGS), what we earn, the gross margin,
// how fast we're burning the DigitalOcean promo credit, and the runway that credit + burn
// imply. SUPERADMIN ONLY (core.Admit).
//
// It FABRICATES NOTHING and OWNS NO cost logic. COGS is the SINGLE source of truth in
// commerce (GET /v1/costs) — cloud CONSUMES it. Revenue + MRR come from commerce billing.
// The one direct vendor read that remains is the DigitalOcean promo-CREDIT balance +
// burn-down history — an ORTHOGONAL treasury view. The derived margin/runway math is a
// pure function (ComputeFinance) with a unit test proving the numbers.
package finance

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"errors"
	"github.com/hanzoai/ai/funding"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/commerce"
	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/admin/iam"
	"github.com/zap-proto/zip"
)

// errUnconfigured marks an upstream that is not wired on this deployment (no DO token /
// no commerce URL). core.SrcOf reports it as a not-ok source so the console shows the
// honest not-configured state rather than a fabricated read.
var errUnconfigured = errors.New("not configured")

// Routes registers the finance dashboard (SuperAdmin only).
func Routes(z *zip.App, s *cloud.Service[core.State]) {
	o := ops{s: s}
	zip.Get(z, "/v1/admin/finance", o.Finance, zip.WithOperationID("adminFinance"))
	// One-time commerce→finance balance cutover (SuperAdmin only). Idempotent per org.
	zip.Post(z, "/v1/admin/finance/backfill", Backfill, zip.WithOperationID("adminFinanceBackfill"))
	// There is no second credit-write here. POST /finance/deposit used to fund an
	// arbitrary subject verbatim, because the credit grant could only reach the org
	// pool; the grant now resolves its address through account.Payer and can name a
	// member, so the raw route's only reason to exist is gone. It was also the unsafe
	// one — no cap, no audit row, and no idempotency ref, so a double-clicked deposit
	// credited twice. ONE credit-write path: core.ApplyGrant.

	// Per-provider upstream credit ledger + usage funding split (multi-provider
	// credit-management). Same SuperAdmin guard, same cloud_usage warehouse.
	zip.Get(z, "/v1/admin/providers/credit", o.ProvidersCredit, zip.WithOperationID("adminProvidersCredit"))
	zip.Get(z, "/v1/admin/usage/funding", o.UsageFunding, zip.WithOperationID("adminUsageFunding"))
}

// ops binds the kernel to the typed handlers: a TypedHandler has no parameter for the
// service, so it arrives as a RECEIVER and every op that reads an upstream is a method
// value. Backfill needs no upstream of ours and stays a plain function.
type ops struct{ s *cloud.Service[core.State] }

// FinanceOut is the GET /v1/admin/finance envelope.
type FinanceOut struct {
	// Status is "ok" or "error". Every upstream on this board degrades in place — an
	// unreachable commerce or an unset DO token answers ok with configured=false and a
	// not-ok source — so an error here means admission itself failed, not the numbers.
	Status string `json:"status"`
	// Msg is the failure, and is empty on success.
	Msg string `json:"msg"`
	// Data is the board. Never null on an ok answer.
	Data *FinanceData `json:"data"`
}

// FinanceData is the full /v1/admin/finance aggregate.
type FinanceData struct {
	// Cost is COGS: what the fleet pays its vendors, plus the DigitalOcean credit
	// treasury view that only runway is derived from.
	Cost FinanceCost `json:"cost"`
	// Revenue is what the fleet earned — consumption and MRR, from commerce.
	Revenue FinanceRevenue `json:"revenue"`
	// Derived is the profitability math over the two above. A pure function of them, so
	// it can be recomputed by hand and checked.
	Derived FinanceDerived `json:"derived"`
	// GeneratedAt is when the read ran, RFC3339. Nothing on this board is cached.
	GeneratedAt string `json:"generatedAt"`
	// Sources is the freshness strip: "commerce-costs", "digitalocean" and "commerce".
	// A not-ok row is the ONLY thing separating a real zero from an upstream that was
	// never read — the margin below is computed either way.
	Sources []core.SourceStatus `json:"sources"`
}

// FinanceCost is the platform COGS view — what WE pay our vendors. Its authority is
// commerce GET /v1/costs: TotalCents is the whole-platform COGS the margin math folds,
// and Vendors is the per-vendor breakdown. Configured is false (and every number 0) when
// commerce /v1/costs is unreachable.
//
// DigitalOcean here is an ORTHOGONAL treasury view (promo-credit remaining + burn-down),
// NOT part of COGS: it feeds only the runway projection.
type FinanceCost struct {
	// Configured reports that commerce answered the COGS read. False makes every number
	// below a zero that was never measured — the margin is still computed from it, so a
	// board showing a healthy margin with configured=false is showing revenue minus
	// nothing.
	Configured bool `json:"configured"`
	// Error is why the COGS read failed, or that commerce is not wired at all. Omitted
	// when Configured is true.
	Error string `json:"error,omitempty"`
	// Period is the billing month the COGS covers, "2006-01". Commerce's own period where
	// it gave one, otherwise the current month.
	Period string `json:"period"`
	// TotalCents is whole-platform COGS for the period, in USD cents — commerce's figure,
	// not a sum of Vendors recomputed here. It is the number the margin subtracts.
	TotalCents int64 `json:"totalCents"`
	// Vendors is the per-vendor breakdown behind that total. Never null; empty when
	// commerce is unreachable.
	Vendors []commerce.Vendor `json:"vendors"`

	// DigitalOcean is the promo-credit treasury view. It is deliberately NOT part of
	// COGS and is never added to TotalCents — the DO spend it reports is already inside
	// the vendor lines above. It feeds runway, and only runway.
	DigitalOcean DoCost `json:"digitalocean"`
}

// DoCost is the DigitalOcean credit + spend view. When Configured is false every number
// is zero and the console renders the honest "connect DO_API_TOKEN" state.
type DoCost struct {
	// Configured reports that a DO_API_TOKEN is set and the balance read succeeded. False
	// zeroes every number below and makes runwayDays null rather than infinite.
	Configured bool `json:"configured"`
	// Error is why the DO read failed, or that the token is unset. Omitted when
	// Configured is true.
	Error string `json:"error,omitempty"`
	// CreditRemainingCents is the promo credit still held, in USD cents, POSITIVE. It is
	// the negated account balance, clamped at zero: DigitalOcean carries an
	// accounts-receivable sign, so credit we hold shows as a NEGATIVE balance and an
	// amount we owe shows positive — an account we owe on has zero credit, not negative.
	CreditRemainingCents int64 `json:"creditRemainingCents"`
	// MonthToDateSpendCents is what DigitalOcean has charged this calendar month, in USD
	// cents. It resets on the first, which is why the burn below divides by the day of
	// the month and not by a fixed 30.
	MonthToDateSpendCents int64 `json:"monthToDateSpendCents"`
	// AvgDailyBurnCents is month-to-date spend divided by the elapsed days of the current
	// month, at least one. A running average, so it is noisiest on the 1st and steadies
	// through the month. It is the denominator of runwayDays.
	AvgDailyBurnCents int64 `json:"avgDailyBurnCents"`
	// AccountBalanceCents is DigitalOcean's raw account balance, sign INCLUDED: positive
	// means we owe DO, negative means we hold credit. It is here so the sign convention
	// is visible rather than only implied by CreditRemainingCents.
	AccountBalanceCents int64 `json:"accountBalanceCents"`
	// GeneratedAt is when DigitalOcean generated the balance, from their response — not
	// when this board read it. Omitted when DO did not say.
	GeneratedAt string `json:"generatedAt,omitempty"`
	// History is the burn-down series, up to the last 60 billing entries. Best effort: a
	// failed history read leaves it empty while the balance above still stands, so an
	// empty series is not evidence of no spend. Never null.
	History []DoHistoryPoint `json:"history"`
}

// DoHistoryPoint is one credit burn-down series point (usage charge over time).
type DoHistoryPoint struct {
	// Date is when DigitalOcean recorded the entry, RFC3339 as they return it.
	Date string `json:"date"`
	// AmountCents is the entry in USD cents, converted once from DO's decimal-dollar
	// string. It carries DO's own accounts-receivable sign: a usage charge is positive.
	AmountCents int64 `json:"amountCents"`
	// Type is DigitalOcean's entry type — "Invoice", "Payment", "Credit". Entries are not
	// all charges, so summing the series without reading this mixes money in with money
	// out.
	Type string `json:"type"`
	// Description is DigitalOcean's own line text for the entry.
	Description string `json:"description"`
}

// FinanceRevenue is the commerce revenue view (all money in USD cents).
type FinanceRevenue struct {
	// Configured reports that the org directory was read and at least the ledger
	// answered. False means the numbers below are zeros nobody measured — and since the
	// margin is revenue minus COGS, that zero reads as a loss rather than as unknown.
	Configured bool `json:"configured"`
	// TotalRevenueCents is fleet consumption over the trailing 30 days, in USD cents,
	// summed across every org. Realized revenue: credit is revenue when it is SPENT, not
	// when it is granted.
	TotalRevenueCents int64 `json:"totalRevenueCents"`
	// MRRCents is fleet monthly recurring revenue, in USD cents, summed over every org's
	// revenue-counting subscriptions. Contract value, and a different question from
	// TotalRevenueCents — it is not folded into the margin.
	MRRCents int64 `json:"mrrCents"`
	// CreditsConsumedCents is the same figure as TotalRevenueCents, named for the other
	// side of the identity: what the fleet earned is exactly what its customers burned.
	CreditsConsumedCents int64 `json:"creditsConsumedCents"`
}

// FinanceDerived is the pure profitability math. Runway is a pointer so it can be null
// (no honest runway when burn is zero or DO is unconfigured).
type FinanceDerived struct {
	// GrossMarginCents is revenue.totalRevenueCents minus cost.totalCents, in USD cents.
	// Negative means the fleet spent more on vendors than its customers consumed.
	GrossMarginCents int64 `json:"grossMarginCents"`
	// GrossMarginPct is that margin as a percentage of revenue — 42.5 means 42.5%, not
	// 0.425. Zero when revenue is zero, which is arithmetic, not a claim of break-even.
	GrossMarginPct float64 `json:"grossMarginPct"`
	// RunwayDays is DigitalOcean promo credit divided by average daily burn — how long
	// the credit lasts at today's rate, and nothing to do with the margin above. NULL,
	// never a large number, when DO is unconfigured or burn is zero: no honest projection
	// exists, and an infinity would read as safety.
	RunwayDays *float64 `json:"runwayDays"`
	// Profitable is revenue strictly greater than COGS — the sign of GrossMarginCents,
	// stated so no consumer has to decide what zero means. It is GROSS margin only:
	// salaries, tooling and anything outside vendor COGS are not in this.
	Profitable bool `json:"profitable"`
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
func (o ops) Finance(ctx context.Context, _ *core.None) (*FinanceOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	data := Compute(o.s, ctx, core.CallerCreds(c))
	return &FinanceOut{Status: core.OK, Data: &data}, nil
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
			credit := max(-int64(bal.Account), 0)
			do.CreditRemainingCents = credit
			do.MonthToDateSpendCents = int64(bal.Usage)
			do.AccountBalanceCents = int64(bal.Account)
			do.GeneratedAt = bal.At
			do.AvgDailyBurnCents = AvgDailyBurnCents(int64(bal.Usage), time.Now().UTC())
			do.History = doHistory(s, ctx)
			sources = append(sources, core.SrcOf("digitalocean", nil, 1, now))

			// Feed the cash circuit-breaker from the SAME numbers this board renders,
			// so the guard and the dashboard can never disagree about whether we are
			// spending real money. On-cash is "the promo grant is gone", which is
			// exactly credit == 0; today's cash is the month-to-date usage attributed
			// to the current day by the same average this board already computes.
			//
			// The ceiling comes from CLOUD_DAILY_CASH_CEILING_CENTS and defaults to 0,
			// which DISARMS the breaker — so this publish is observational until an
			// operator sets a number. See ai/internal/funding.
			funding.Publish(funding.State{
				OnCash:       credit <= 0,
				TodayCents:   do.AvgDailyBurnCents,
				CeilingCents: dailyCashCeilingCents(),
			})
		}
	}
	if do.History == nil {
		do.History = []DoHistoryPoint{}
	}
	cost.DigitalOcean = do

	// ── Revenue: commerce (fleet-wide) ────────────────────────────────────
	// Consumption comes from the ONE per-org money read (core.OrgMoney), which asks the
	// process that owns the ledger. This board used to carry its own copy of that read
	// against commerce's HTTP /v1/billing/usage/rollup — a route registered in no binary
	// here, so it answered 404 for every org and the margin was computed from a revenue
	// of zero. MRR stays a subscriptions read; its absence degrades the row, never the
	// money source.
	rev := FinanceRevenue{}
	if orgs, orgErr := core.ListOrgs(s, ctx, cr); orgErr != nil {
		// The revenue source is unreadable → say so, never a zero that would flip the
		// margin negative on an upstream hiccup.
		sources = append(sources, core.SrcOf("commerce", orgErr, 0, now))
	} else {
		var totalRev, mrr int64
		var partial, ledger bool
		money := core.Delegate(ctx)
		for _, o := range orgs {
			spend, _, e := core.OrgMoney(s, money, o.Name)
			switch {
			case core.MoneyFailed(e):
				partial, ledger = true, true
			case e == nil:
				totalRev += spend
				ledger = true
			}
			if pl, e := s.State.Commerce.Plan(ctx, o.Name); e == nil {
				mrr += int64(pl.MRR)
			} else {
				partial = true
			}
		}
		switch {
		case len(orgs) > 0 && !ledger:
			sources = append(sources, core.SrcOf("commerce", core.ErrNoLedger, 0, now))
		case partial:
			rev.Configured = true
			sources = append(sources, core.SrcOf("commerce", core.ErrPartialRevenue, len(orgs), now))
		default:
			rev.Configured = true
			sources = append(sources, core.SrcOf("commerce", nil, len(orgs), now))
		}
		rev.TotalRevenueCents = totalRev
		rev.CreditsConsumedCents = totalRev
		rev.MRRCents = mrr
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
	day := max(now.Day(), 1)
	return monthToDateSpendCents / int64(day)
}
