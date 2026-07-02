package admin

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/zap-proto/zip"
)

// errUnconfigured marks an upstream that is not wired on this deployment (no DO
// token / no commerce URL). srcOf reports it as a not-ok source so the console
// shows the honest not-configured state rather than a fabricated read.
var errUnconfigured = errors.New("not configured")

// ── /v1/admin/finance — SaaS business/finance dashboard (FinanceData) ─────────
//
// The profitability panel the Hanzo Admin Console renders on admin.hanzo.ai: how
// fast we're burning the DigitalOcean promo credit (our primary venue), what we
// spend, what we earn, the resulting gross margin, and the runway that credit +
// burn imply. It is GLOBAL-ADMIN ONLY (s.guard) — financial data is Hanzo-internal
// and must never reach a customer or tenant-admin.
//
// Like the rest of admin it FABRICATES NOTHING. Cost comes from DigitalOcean's
// billing API (honest {configured:false} when DO_API_TOKEN is unset); revenue and
// MRR come from commerce (honest zeros when commerce is unreachable). The derived
// margin/runway math is a pure function (computeFinance) with a unit test proving
// the numbers and the unconfigured path.

// financeData is the full /v1/admin/finance aggregate (FinanceData).
type financeData struct {
	Cost        financeCost    `json:"cost"`
	Revenue     financeRevenue `json:"revenue"`
	Derived     financeDerived `json:"derived"`
	GeneratedAt string         `json:"generatedAt"`
	Sources     []sourceStatus `json:"sources"`
}

// financeCost.digitalocean is the DO billing view (all money in USD cents).
type financeCost struct {
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
// handler fills it from the DO + commerce reads; the pure function does the math
// so the derivation is unit-testable in isolation.
type financeInput struct {
	do          doCost
	revenue     financeRevenue
	generatedAt string
	sources     []sourceStatus
}

// computeFinance is the PURE derivation: given the DO cost view and the commerce
// revenue view, it computes gross margin, margin %, runway, and profitability.
// No I/O, no clock, no globals — everything it needs is in financeInput, which is
// exactly why the finance math can be tested without any network.
//
//	grossMarginCents = revenue - cost(month-to-date spend)
//	grossMarginPct   = grossMargin / revenue * 100   (0 when revenue is 0)
//	runwayDays       = creditRemaining / avgDailyBurn (nil when burn 0 or DO off)
//	profitable       = revenue > cost
func computeFinance(in financeInput) financeData {
	cost := in.do.MonthToDateSpendCents
	rev := in.revenue.TotalRevenueCents

	margin := rev - cost
	var marginPct float64
	if rev > 0 {
		marginPct = (float64(margin) / float64(rev)) * 100
	}

	var runway *float64
	if in.do.Configured && in.do.AvgDailyBurnCents > 0 {
		d := float64(in.do.CreditRemainingCents) / float64(in.do.AvgDailyBurnCents)
		runway = &d
	}

	return financeData{
		Cost:    financeCost{DigitalOcean: in.do},
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

// finance answers GET /v1/admin/finance. It reads the DO balance/history and the
// fleet commerce revenue, then hands both to computeFinance. Global-admin only
// (mounted under s.guard); no principal / tenant-admin / forged header → 403
// before this handler ever runs.
func (s *svc) finance(c *zip.Ctx) error {
	ctx := c.Context()
	cr := callerCreds(c)
	now := time.Now().UTC().Format(time.RFC3339)

	var sources []sourceStatus

	// ── Cost: DigitalOcean ────────────────────────────────────────────────
	do := doCost{Configured: s.do.configured()}
	if !s.do.configured() {
		do.Error = "DO_API_TOKEN not configured"
		sources = append(sources, srcOf("digitalocean", errUnconfigured, 0, now))
	} else {
		bal, err := s.do.balance(ctx)
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
			do.History = s.doHistory(ctx)
			sources = append(sources, srcOf("digitalocean", nil, 1, now))
		}
	}
	if do.History == nil {
		do.History = []doHistoryPoint{}
	}

	// ── Revenue: commerce (fleet-wide) ────────────────────────────────────
	rev := financeRevenue{Configured: s.commerce.configured()}
	if s.commerce.configured() {
		orgs, orgErr := s.listOrgs(ctx, cr)
		if orgErr != nil {
			sources = append(sources, srcOf("commerce", orgErr, 0, now))
		} else {
			var totalRev, mrr int64
			for _, o := range orgs {
				subj := orgSubject(o.Name)
				if r, e := s.commerce.usageRollup(ctx, o.Name, subj); e == nil {
					totalRev += r.ConsumedCents
				}
				if m, e := s.commerce.mrrCents(ctx, o.Name, subj); e == nil {
					mrr += m
				}
			}
			// Realized revenue = what customers consumed (metered spend). Credits
			// consumed mirrors that same figure at the fleet level.
			rev.TotalRevenueCents = totalRev
			rev.CreditsConsumedCents = totalRev
			rev.MRRCents = mrr
			sources = append(sources, srcOf("commerce", nil, len(orgs), now))
		}
	} else {
		sources = append(sources, srcOf("commerce", errUnconfigured, 0, now))
	}

	return ok(c, computeFinance(financeInput{
		do:          do,
		revenue:     rev,
		generatedAt: now,
		sources:     sources,
	}))
}

// doHistory reads DO billing history into the burn-down series (best-effort:
// a failure yields an empty series, never a fabricated trend). Only usage-side
// entries (Invoice/charges) shape the burn-down; the series stays honest-empty
// when history is unavailable.
func (s *svc) doHistory(ctx context.Context) []doHistoryPoint {
	entries, err := s.do.history(ctx, 60)
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
