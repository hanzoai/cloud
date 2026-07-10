package admin

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// errUnconfigured marks an upstream that is not wired on this deployment (no DO
// token / no commerce URL). srcOf reports it as a not-ok source so the console
// shows the honest not-configured state rather than a fabricated read.
var errUnconfigured = errors.New("not configured")

// errPartialRevenue marks a revenue read that succeeded at the org-list level but
// had one or more per-org failures — the fleet total is real but PARTIAL. srcOf
// reports it as a not-ok source so the console shows a degraded state rather than
// presenting an under-count as authoritative.
var errPartialRevenue = errors.New("partial: one or more org revenue reads failed")

// ── /v1/admin/finance — SaaS business/finance dashboard (FinanceData) ─────────
//
// The profitability panel the Hanzo Admin Console renders on admin.hanzo.ai: what
// we pay every vendor (COGS), what we earn, the resulting gross margin, how fast
// we're burning the DigitalOcean promo credit, and the runway that credit + burn
// imply. It is GLOBAL-ADMIN ONLY (s.guard) — financial data is Hanzo-internal and
// must never reach a customer or tenant-admin.
//
// Like the rest of admin it FABRICATES NOTHING and it OWNS NO cost logic. COGS is
// the SINGLE source of truth in commerce (GET /v1/costs: DigitalOcean compute +
// the LLM providers we resell) — cloud CONSUMES it, never re-reads a vendor's
// billing API to derive a cost, so the margin uses the whole multi-vendor COGS.
// Revenue + MRR come from commerce billing (honest zeros when unreachable). The
// one direct vendor read that remains is the DigitalOcean promo-CREDIT balance +
// burn-down history — an ORTHOGONAL treasury view (how long the credit lasts), NOT
// a COGS: commerce tracks what we SPEND with DO (the compute line), not our prepaid
// credit balance, so it can't provide it. The derived margin/runway math is a pure
// function (computeFinance) with a unit test proving the numbers and every
// unconfigured path.

// financeData is the full /v1/admin/finance aggregate (FinanceData).
type financeData struct {
	Cost        financeCost    `json:"cost"`
	Revenue     financeRevenue `json:"revenue"`
	Derived     financeDerived `json:"derived"`
	GeneratedAt string         `json:"generatedAt"`
	Sources     []sourceStatus `json:"sources"`
}

// financeCost is the platform COGS view — what WE pay our vendors. Its authority
// is commerce GET /v1/costs (the SINGLE vendor-COGS source of truth): TotalCents is
// the whole-platform COGS the margin math folds, and Vendors is the per-vendor
// breakdown (DigitalOcean compute + each LLM provider we resell) the console
// renders as a donut. Configured is false (and every number 0) when commerce
// /v1/costs is unreachable — the console then shows the honest not-configured state.
//
// DigitalOcean here is an ORTHOGONAL treasury view (promo-credit remaining + the
// burn-down series), NOT part of COGS: its month-to-date spend is NO LONGER the
// margin cost (TotalCents is) — it feeds only the runway projection. Commerce owns
// the DO compute COGS line; this is our prepaid-credit balance, which commerce
// does not track, so it stays a direct DO account read.
type financeCost struct {
	Configured bool         `json:"configured"`
	Error      string       `json:"error,omitempty"`
	Period     string       `json:"period"`
	TotalCents int64        `json:"totalCents"`
	Vendors    []vendorCost `json:"vendors"`

	DigitalOcean doCost `json:"digitalocean"`
}

// doCost is the DigitalOcean credit + spend view. When Configured is false every
// number is zero and the console renders the honest "connect DO_API_TOKEN" state.
type doCost struct {
	Configured            bool             `json:"configured"`
	Error                 string           `json:"error,omitempty"`
	CreditRemainingCents  int64            `json:"creditRemainingCents"`
	MonthToDateSpendCents int64            `json:"monthToDateSpendCents"`
	AvgDailyBurnCents     int64            `json:"avgDailyBurnCents"`
	AccountBalanceCents   int64            `json:"accountBalanceCents"`
	GeneratedAt           string           `json:"generatedAt,omitempty"`
	History               []doHistoryPoint `json:"history"`
}

// doHistoryPoint is one credit burn-down series point (usage charge over time).
type doHistoryPoint struct {
	Date        string `json:"date"`
	AmountCents int64  `json:"amountCents"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

// financeRevenue is the commerce revenue view (all money in USD cents).
type financeRevenue struct {
	Configured           bool  `json:"configured"`
	TotalRevenueCents    int64 `json:"totalRevenueCents"`
	MRRCents             int64 `json:"mrrCents"`
	CreditsConsumedCents int64 `json:"creditsConsumedCents"`
}

// financeDerived is the pure profitability math. Runway is a pointer so it can be
// null (no honest runway when burn is zero or DO is unconfigured).
type financeDerived struct {
	GrossMarginCents int64    `json:"grossMarginCents"`
	GrossMarginPct   float64  `json:"grossMarginPct"`
	RunwayDays       *float64 `json:"runwayDays"`
	Profitable       bool     `json:"profitable"`
}

// financeInput is the raw material computeFinance folds into financeData. The
// handler fills cost from the commerce COGS read (+ the DO-credit treasury view)
// and revenue from commerce billing; the pure function does the math so the
// derivation is unit-testable in isolation.
type financeInput struct {
	cost        financeCost
	revenue     financeRevenue
	generatedAt string
	sources     []sourceStatus
}

// computeFinance is the PURE derivation: given the multi-vendor COGS view and the
// commerce revenue view, it computes gross margin, margin %, runway, and
// profitability. No I/O, no clock, no globals — everything it needs is in
// financeInput, which is exactly why the finance math can be tested without any
// network.
//
//	grossMarginCents = revenue - COGS(total, all vendors)
//	grossMarginPct   = grossMargin / revenue * 100          (0 when revenue is 0)
//	runwayDays       = DO creditRemaining / DO avgDailyBurn  (nil when burn 0 or DO off)
//	profitable       = revenue > COGS
func computeFinance(in financeInput) financeData {
	cost := in.cost.TotalCents
	rev := in.revenue.TotalRevenueCents

	margin := rev - cost
	var marginPct float64
	if rev > 0 {
		marginPct = (float64(margin) / float64(rev)) * 100
	}

	// Runway is the DO promo-credit treasury projection (orthogonal to COGS): how
	// many days the remaining credit lasts at the current DO burn. Nil when DO is
	// off or burn is 0 — never a fabricated infinity.
	do := in.cost.DigitalOcean
	var runway *float64
	if do.Configured && do.AvgDailyBurnCents > 0 {
		d := float64(do.CreditRemainingCents) / float64(do.AvgDailyBurnCents)
		runway = &d
	}

	return financeData{
		Cost:    in.cost,
		Revenue: in.revenue,
		Derived: financeDerived{
			GrossMarginCents: margin,
			GrossMarginPct:   marginPct,
			RunwayDays:       runway,
			Profitable:       rev > cost,
		},
		GeneratedAt: in.generatedAt,
		Sources:     in.sources,
	}
}

// finance answers GET /v1/admin/finance. It reads the multi-vendor COGS from
// commerce /v1/costs, the DO promo-credit/burn-down treasury view, and the fleet
// commerce revenue, then hands them to computeFinance. Global-admin only (mounted
// under s.guard); no principal / tenant-admin / forged header → 403 before this
// handler ever runs.
func finance(s *cloud.Service[state], c *zip.Ctx) error {
	ctx := c.Context()
	cr := callerCreds(c)
	now := time.Now().UTC().Format(time.RFC3339)
	period := time.Now().UTC().Format("2006-01")

	var sources []sourceStatus

	// ── COGS: commerce /v1/costs (the single vendor-COGS source of truth) ──
	// cloud CONSUMES the multi-vendor breakdown (DigitalOcean compute + the LLM
	// providers we resell) — it does NOT re-derive any vendor cost. TotalCents is
	// the margin cost. Honest not-configured when commerce is unreachable.
	cost := financeCost{Period: period}
	if s.State.commerce.configured() {
		report, err := s.State.commerce.costs(ctx, period)
		if err != nil {
			cost.Error = err.Error()
			sources = append(sources, srcOf("commerce-costs", err, 0, now))
		} else {
			cost.Configured = true
			cost.TotalCents = report.TotalCents
			cost.Vendors = report.Vendors
			if report.Period != "" {
				cost.Period = report.Period
			}
			sources = append(sources, srcOf("commerce-costs", nil, len(report.Vendors), now))
		}
	} else {
		cost.Error = "commerce /v1/costs not configured"
		sources = append(sources, srcOf("commerce-costs", errUnconfigured, 0, now))
	}
	if cost.Vendors == nil {
		cost.Vendors = []vendorCost{}
	}

	// ── DigitalOcean promo-credit / runway (orthogonal treasury view) ──
	// The one direct vendor read that remains: our DO prepaid-credit balance +
	// burn-down history, which commerce does not track. Its MTD spend feeds ONLY
	// the runway projection — it is NOT the margin cost (that is cost.TotalCents).
	do := doCost{Configured: s.State.do.configured()}
	if !s.State.do.configured() {
		do.Error = "DO_API_TOKEN not configured"
		sources = append(sources, srcOf("digitalocean", errUnconfigured, 0, now))
	} else {
		bal, err := s.State.do.balance(ctx)
		if err != nil {
			do.Error = err.Error()
			sources = append(sources, srcOf("digitalocean", err, 0, now))
		} else {
			// creditRemaining = -account_balance clamped at 0 (negative account
			// balance = credit we hold; a positive balance means we owe DO → 0 credit).
			credit := -bal.AccountBalanceCents
			if credit < 0 {
				credit = 0
			}
			do.CreditRemainingCents = credit
			do.MonthToDateSpendCents = bal.MonthToDateUsageCents
			do.AccountBalanceCents = bal.AccountBalanceCents
			do.GeneratedAt = bal.GeneratedAt
			do.AvgDailyBurnCents = avgDailyBurnCents(bal.MonthToDateUsageCents, time.Now().UTC())
			do.History = doHistory(s, ctx)
			sources = append(sources, srcOf("digitalocean", nil, 1, now))
		}
	}
	if do.History == nil {
		do.History = []doHistoryPoint{}
	}
	cost.DigitalOcean = do

	// ── Revenue: commerce (fleet-wide) ────────────────────────────────────
	// Configured means the revenue source was actually READ, not merely wired: on a
	// transient IAM/commerce failure it stays FALSE so computeFinance and the console
	// never fabricate a negative margin / red "burning" alarm from a fake zero.
	rev := financeRevenue{}
	if !s.State.commerce.configured() {
		sources = append(sources, srcOf("commerce", errUnconfigured, 0, now))
	} else if orgs, orgErr := listOrgs(s, ctx, cr); orgErr != nil {
		// The revenue source is unreadable → honest not-configured, never a zero
		// that would flip the margin negative on an upstream hiccup.
		sources = append(sources, srcOf("commerce", orgErr, 0, now))
	} else {
		var totalRev, mrr int64
		partial := false
		for _, o := range orgs {
			subj := orgSubject(o.Name)
			if r, e := s.State.commerce.usageRollup(ctx, o.Name, subj); e == nil {
				totalRev += r.ConsumedCents
			} else {
				partial = true
			}
			if m, e := s.State.commerce.mrrCents(ctx, o.Name, subj); e == nil {
				mrr += m
			} else {
				partial = true
			}
		}
		// Realized revenue = what customers consumed (metered spend). Credits
		// consumed mirrors that same figure at the fleet level.
		rev.Configured = true
		rev.TotalRevenueCents = totalRev
		rev.CreditsConsumedCents = totalRev
		rev.MRRCents = mrr
		// A per-org read failure means the fleet total is PARTIAL — mark the source
		// not-ok so the console shows a degraded state, never presents an under-count
		// as authoritative.
		if partial {
			sources = append(sources, srcOf("commerce", errPartialRevenue, len(orgs), now))
		} else {
			sources = append(sources, srcOf("commerce", nil, len(orgs), now))
		}
	}

	return ok(c, computeFinance(financeInput{
		cost:        cost,
		revenue:     rev,
		generatedAt: now,
		sources:     sources,
	}))
}

// doHistory reads DO billing history into the burn-down series (best-effort:
// a failure yields an empty series, never a fabricated trend). Only usage-side
// entries (Invoice/charges) shape the burn-down; the series stays honest-empty
// when history is unavailable.
func doHistory(s *cloud.Service[state], ctx context.Context) []doHistoryPoint {
	entries, err := s.State.do.history(ctx, 60)
	if err != nil {
		return []doHistoryPoint{}
	}
	pts := make([]doHistoryPoint, 0, len(entries))
	for _, e := range entries {
		pts = append(pts, doHistoryPoint{
			Date:        e.Date,
			AmountCents: e.AmountCents,
			Type:        e.Type,
			Description: e.Description,
		})
	}
	return pts
}

// avgDailyBurnCents derives the average daily DO burn from month-to-date usage:
// month-to-date spend divided by the number of elapsed days in the current month
// (at least 1, so day 1 doesn't divide by zero). This is the honest run-rate the
// runway projection uses — a real read (MTD usage) over real elapsed time, never
// an invented rate.
func avgDailyBurnCents(monthToDateSpendCents int64, now time.Time) int64 {
	day := now.Day()
	if day < 1 {
		day = 1
	}
	return monthToDateSpendCents / int64(day)
}

// doTokenFromEnv reads the DigitalOcean token from the environment. Sourced from
// a KMSSecret on the cloud deployment (DO_API_TOKEN) — never hard-coded.
func doTokenFromEnv() string {
	return strings.TrimSpace(os.Getenv("DO_API_TOKEN"))
}
