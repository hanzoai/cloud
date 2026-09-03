// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package admin

// moneyboard — GET /v1/admin/money, the ONE consolidated financial view.
//
// The money answer used to be spread across four boards an operator had to read
// side by side and add up by hand: /v1/admin/revenue (what customers hold and
// spend), /v1/admin/finance (what we pay vendors, and the margin), /v1/admin/grants
// (credits we issued), and the infra board's monthly burn. This is those four folded
// into one payload — revenue, credits granted vs consumed, spend by org, outstanding
// balance, and infrastructure cost.
//
// It is an AGGREGATOR OF AGGREGATORS and adds no new arithmetic: it calls the very
// functions those endpoints call — revenue.Compute, finance.Compute,
// customer.GrantRows — so the consolidated total can never disagree with the board it
// came from. That is why each of those was split into compute + handler rather than
// copied: one aggregation, two views.
//
// Every source keeps its own freshness row, namespaced by the domain that read it (a
// bare "commerce" appears under both revenue and finance and would otherwise collide),
// so a degraded upstream is visible as a degraded LINE rather than silently folded
// into an authoritative-looking total.
//
// SUPERADMIN ONLY (core.Guard) — it crosses every tenant. Money is integer USD cents
// end to end (money.Cents); nothing here is ever a float.

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/commerce"
	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/admin/customer"
	"github.com/hanzoai/cloud/apps/admin/finance"
	"github.com/hanzoai/cloud/apps/admin/revenue"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// moneyGrantScan bounds the audit scan behind the grant totals. Grants are staff-issued
// and low-volume, so this covers the real history; if it is ever hit, the grants source
// is reported PARTIAL rather than presenting a truncated sum as complete.
const moneyGrantScan = 5000

// The two ways the grant total can be less than the whole truth. Both surface through
// core.SrcOf as a not-ok source, so the console shows a degraded line instead of an
// undercount that looks authoritative.
var (
	errNoAuditStore       = errors.New("no local audit store configured on this deployment")
	errGrantScanTruncated = errors.New("partial: grant history exceeded the scan bound")
)

// moneyBoard is the whole GET /v1/admin/money payload.
type moneyBoard struct {
	// Revenue is what customers actually pay: realized consumption, recurring
	// contract value, and the counts behind them.
	Revenue moneyRevenue `json:"revenue"`
	// Credits is the granted-against-consumed ledger, including the balance
	// customers still hold.
	Credits moneyCredits `json:"credits"`
	// Infra is what the platform costs to run: vendor COGS, the DigitalOcean
	// burn-down, and the reserve backing the payout programs.
	Infra moneyInfra `json:"infrastructure"`
	// Margin is revenue against cost, carried through from the finance board
	// unchanged so the two can never report different margins.
	Margin moneyMargin `json:"margin"`
	// ByOrg is one line per customer, biggest spender first. The row SET comes
	// from revenue, so an org that was granted credit but no longer exists in IAM
	// is inside the fleet totals above and has no line of its own.
	ByOrg []moneyOrgRow `json:"byOrg"`
	// GeneratedAt is when the board was assembled, RFC 3339 UTC. Nothing here is
	// cached.
	GeneratedAt string `json:"generatedAt"`
	// Sources is one row per upstream, each name prefixed by the domain that read
	// it (revenue:, finance:, grants:audit, treasury) — a bare "commerce" appears
	// under two domains and would otherwise collide. A not-ok row means the total
	// it feeds is an UNDERCOUNT, not a smaller number.
	Sources []core.SourceStatus `json:"sources"`
}

// moneyRevenue is what customers actually pay us.
type moneyRevenue struct {
	// RealizedCents is credit customers have actually CONSUMED, fleet-wide, in US
	// cents. This is the revenue figure — MRR beside it is contract value and the
	// two are never added.
	RealizedCents money.Cents `json:"realizedCents"`
	// MRRCents is fleet monthly recurring revenue, in US cents — contract value
	// from subscriptions. Orthogonal to RealizedCents and never added to it.
	MRRCents money.Cents `json:"mrrCents"`
	// ARRCents is MRRCents times twelve, in US cents. A projection of today's
	// contracts, not a measurement of a year.
	ARRCents money.Cents `json:"arrCents"`
	// ARPUCents is realized spend divided by PAYING customers, in US cents — per
	// paying customer, not per signup, so free signups cannot deflate it.
	ARPUCents money.Cents `json:"arpuCents"`
	// Customers is every org IAM lists, paying or not.
	Customers int `json:"customers"`
	// Paying is how many of them have spend or MRR above zero. It is the ARPU
	// denominator.
	Paying int `json:"paying"`
}

// moneyCredits is the granted-vs-consumed ledger. Outstanding is the liability side:
// prepaid + promo balance customers still hold and have not yet burned.
type moneyCredits struct {
	// GrantedCents is every credit successfully issued, in US cents, over the
	// whole grant history rather than a window. Refused grants are excluded — they
	// moved nothing.
	GrantedCents money.Cents `json:"grantedCents"`
	// GrantedTrialCents is the non-cash half — comps and promos we gave away — in
	// US cents. A grant whose source the ledger did not record counts here, since a
	// grant nobody paid for is the safer assumption.
	GrantedTrialCents money.Cents `json:"grantedTrialCents"`
	// GrantedPrepaidCents is the half backed by real money the customer paid in, in
	// US cents. With GrantedTrialCents it sums to GrantedCents.
	GrantedPrepaidCents money.Cents `json:"grantedPrepaidCents"`
	// ConsumedCents is realized spend, in US cents. A credit is consumed exactly
	// when it is spent, so this is the same number revenue reports, stated once
	// from one source.
	ConsumedCents money.Cents `json:"consumedCents"`
	// OutstandingCents is the LIABILITY: prepaid and promo balance customers still
	// hold and have not burned. It is not revenue, and it is not GrantedCents
	// minus ConsumedCents — customers also add their own money.
	OutstandingCents money.Cents `json:"outstandingCents"`
	// Grants is how many successful grants make up GrantedCents. If it reaches the
	// scan bound the grants source is marked partial rather than the sum being
	// presented as complete.
	Grants int `json:"grants"`
}

// moneyInfra is what the platform costs to run: vendor COGS plus the DigitalOcean
// burn-down, plus the platform reserve fund backing the payout programs.
type moneyInfra struct {
	// Period is the billing month the vendor COGS covers, "2006-01", as commerce
	// reports it. It bounds VendorCogsCents only — the DigitalOcean figures below
	// are month-to-date and the reserve is point-in-time.
	Period string `json:"period"`
	// VendorCogsCents is what the platform owes its vendors for Period, in US
	// cents, POSITIVE — money out is not negated. This is the COGS the margin
	// subtracts.
	VendorCogsCents money.Cents `json:"vendorCogsCents"`
	// DOMonthToDateCents is DigitalOcean spend so far this month, in US cents.
	DOMonthToDateCents money.Cents `json:"doMonthToDateCents"`
	// DOCreditRemainingCents is the promo credit left on the DigitalOcean account,
	// in US cents — what is left to spend, not what was granted.
	DOCreditRemainingCents money.Cents `json:"doCreditRemainingCents"`
	// DOAvgDailyBurnCents is the average DigitalOcean spend per day, in US cents.
	// Dividing the credit above by it is the runway.
	DOAvgDailyBurnCents money.Cents `json:"doAvgDailyBurnCents"`
	// TreasuryReserveCents is the platform reserve backing the payout programs, in
	// US cents, read live from the treasury app. Zero here can mean an empty
	// reserve OR an unreachable treasury — the treasury row in sources is what
	// tells them apart.
	TreasuryReserveCents money.Cents `json:"treasuryReserveCents"`
	// Vendors is the COGS broken out per vendor and service line for Period, each
	// amount positive.
	Vendors []commerce.Vendor `json:"vendors"`
}

// moneyMargin is revenue against cost, carried through from finance so the two boards
// cannot report different margins.
type moneyMargin struct {
	// GrossCents is realized revenue minus vendor COGS, in US cents. Negative
	// means the fleet spent more on vendors than its customers consumed. GROSS
	// only: salaries, tooling and everything outside vendor COGS are not in it.
	GrossCents money.Cents `json:"grossCents"`
	// GrossPct is that margin as a percentage of revenue — 42.5 means 42.5%, not
	// 0.425. Zero when revenue is zero, which is arithmetic rather than a claim of
	// break-even.
	GrossPct float64 `json:"grossPct"`
	// Profitable is revenue strictly greater than COGS — the sign of GrossCents
	// stated outright, so no consumer has to decide what zero means.
	Profitable bool `json:"profitable"`
	// RunwayDays is DigitalOcean promo credit divided by average daily burn: how
	// long the credit lasts at today's rate, and nothing to do with the margin
	// above. NULL — never a large number — when DigitalOcean is unconfigured or
	// burn is zero, because no honest projection exists and an infinity would read
	// as safety.
	RunwayDays *float64 `json:"runwayDays"`
}

// moneyOrgRow is one customer's whole money position on a single line — what they were
// given, what they spent, and what they still hold.
type moneyOrgRow struct {
	// Org is the tenant slug — the row's identity.
	Org string `json:"org"`
	// Display is the org's display name, falling back to the slug.
	Display string `json:"display"`
	// Plan is the live subscription tier's name, or "pay-as-you-go" — which is
	// also what an org reads as when the subscriptions read failed, so it is not
	// proof of no plan.
	Plan string `json:"plan"`
	// SpendCents is realized consumption over the trailing 30 days, in US cents.
	// The board sorts on it, largest first.
	SpendCents money.Cents `json:"spendCents"`
	// BalanceCents is the prepaid wallet the tenant still holds, in US cents —
	// money not yet earned.
	BalanceCents money.Cents `json:"balanceCents"`
	// MRRCents is the tenant's monthly recurring revenue, in US cents. Contract
	// value, not cash collected.
	MRRCents money.Cents `json:"mrrCents"`
	// GrantedCents is credit successfully granted TO this tenant, in US cents,
	// across all history. Zero for a tenant that was never granted any.
	GrantedCents money.Cents `json:"grantedCents"`
	// Grants is how many grants that was.
	Grants int `json:"grants"`
}

// MoneyOut is the GET /v1/admin/money envelope.
type MoneyOut struct {
	// Status is "ok" or "error", at HTTP 200 either way. A degraded upstream still
	// answers "ok" — it is reported in data.sources, because a consolidated board
	// that fails whole because one source is down is useless.
	Status string `json:"status"`
	// Msg is the failure reason when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data is the consolidated board. Omitted when the caller was refused.
	Data *moneyBoard `json:"data,omitempty"`
}

// moneyBoardHandler answers GET /v1/admin/money.
func (o ops) Money(ctx context.Context, _ *core.None) (*MoneyOut, error) {
	c, err := core.Admit(ctx)
	if err != nil {
		return nil, err
	}
	// Money is read per org, and three of these folds run their orgs in parallel, so
	// the principal is lifted off the request ONCE here and re-pointed per tenant.
	ctx = core.Acting(c)
	s := o.s
	at := time.Now().UTC().Format(time.RFC3339)

	rev, revErr := revenue.Compute(s, ctx, core.CallerCreds(c))
	fin := finance.Compute(s, ctx, core.CallerCreds(c))
	grants, _, grantErr := customer.GrantRows(s, ctx, customer.GrantFilter("", "success", moneyGrantScan))

	held, reserveErr := reserve(ctx, c)
	board := foldMoney(rev, fin, grants, held, at)

	// Freshness, namespaced by the domain that read it.
	board.Sources = []core.SourceStatus{}
	if revErr != nil {
		board.Sources = append(board.Sources, core.SrcOf("revenue", revErr, 0, at))
	} else {
		board.Sources = mergeSources(board.Sources, "revenue", rev.Sources)
	}
	board.Sources = mergeSources(board.Sources, "finance", fin.Sources)
	board.Sources = append(board.Sources, grantSource(s, grants, grantErr, at))
	// The reserve is READ FROM ANOTHER APP, so it gets a freshness row like every
	// other remote read. Before this it silently rendered 0 whenever treasury was
	// not in the binary — which, since admin is its own binary, was always.
	board.Sources = append(board.Sources, core.SrcOf("treasury", reserveErr, 1, at))

	return &MoneyOut{Status: core.OK, Data: &board}, nil
}

// reserve reads the platform reserve FROM THE TREASURY APP, over its unix
// socket, as native ZAP (cloud.Dial + rpc.go): one frame out, one frame in,
// and the int64 is read straight out of the reply's wire bytes — no JSON on
// the path at either end.
//
// It used to be an import — treasury.ReserveCents(ctx) — which resolved
// against treasury's `mounted` package global. admin is its own binary, so
// that call returned 0 every time, and the board printed a zero balance and
// called it the truth. The error is RETURNED, not swallowed, so the board can
// say "could not reach the treasury" instead of "you have no money" — the
// distinction the import could not make.
//
// c carries the SuperAdmin principal core.Admit already validated; it rides
// the envelope's capability slot and treasury re-checks it on its side —
// delegation, never escalation.
func reserve(ctx context.Context, c *zip.Ctx) (money.Cents, error) {
	out, err := cloud.Ask[struct{}, plane.Reserved](cloud.As(c, ""), "treasury",
		plane.TreasuryReserve, &struct{}{})
	if err != nil {
		return 0, err
	}
	if out == nil {
		return 0, nil
	}
	// ROUND, because this is a DISPLAY and Minor() refuses a sub-cent amount.
	//
	// The treasury reserve carries the same eighteen-decimal tail every ledger
	// amount does, so Minor() answers "is finer than its minor unit" and the board
	// rendered that parse failure as SrcOf("treasury", err) — "could not reach the
	// treasury" — for a treasury that was reachable and correct. Nothing is billed
	// from this number; a half-cent either way in a headline figure is not a
	// number anyone spends, and being unable to show the figure at all is worse.
	cents, err := out.Amount.RoundMinor()
	return money.Cents(cents), err
}

// grantSource reports grant freshness, including the two ways the total can be less
// than the whole truth: no audit store at all, and a scan that hit its bound.
func grantSource(s *cloud.Service[core.State], grants []customer.GrantRow, err error, at string) core.SourceStatus {
	switch {
	case err != nil:
		return core.SrcOf("grants:audit", err, 0, at)
	case s.State.AuditStore == nil:
		return core.SrcOf("grants:audit", errNoAuditStore, 0, at)
	case len(grants) >= moneyGrantScan:
		return core.SrcOf("grants:audit", errGrantScanTruncated, len(grants), at)
	default:
		return core.SrcOf("grants:audit", nil, len(grants), at)
	}
}

// ── pure folds (unit-tested) ──

// foldMoney assembles the board from the three domain aggregates. Pure: every input is
// already-read data, so the whole shape of the answer is testable without an upstream.
func foldMoney(rev revenue.RevenueData, fin finance.FinanceData, grants []customer.GrantRow, reserve money.Cents, at string) moneyBoard {
	granted, byOrg := foldGrants(grants)

	return moneyBoard{
		Revenue: moneyRevenue{
			RealizedCents: money.Cents(rev.TotalSpendCents),
			MRRCents:      money.Cents(rev.MRRCents),
			ARRCents:      money.Cents(rev.MRRCents * 12),
			ARPUCents:     money.Cents(rev.ARPUCents),
			Customers:     rev.Customers,
			Paying:        rev.PayingCustomers,
		},
		Credits: moneyCredits{
			GrantedCents:        granted.total,
			GrantedTrialCents:   granted.trial,
			GrantedPrepaidCents: granted.prepaid,
			// Consumed is realized spend: a credit is consumed exactly when it is spent,
			// so this is the same number revenue reports — stated once, from one source.
			ConsumedCents:    money.Cents(rev.TotalSpendCents),
			OutstandingCents: money.Cents(rev.TotalBalancesCents),
			Grants:           granted.count,
		},
		Infra: moneyInfra{
			Period:                 fin.Cost.Period,
			VendorCogsCents:        money.Cents(fin.Cost.TotalCents),
			DOMonthToDateCents:     money.Cents(fin.Cost.DigitalOcean.MonthToDateSpendCents),
			DOCreditRemainingCents: money.Cents(fin.Cost.DigitalOcean.CreditRemainingCents),
			DOAvgDailyBurnCents:    money.Cents(fin.Cost.DigitalOcean.AvgDailyBurnCents),
			TreasuryReserveCents:   reserve,
			Vendors:                fin.Cost.Vendors,
		},
		Margin: moneyMargin{
			GrossCents: money.Cents(fin.Derived.GrossMarginCents),
			GrossPct:   fin.Derived.GrossMarginPct,
			Profitable: fin.Derived.Profitable,
			RunwayDays: fin.Derived.RunwayDays,
		},
		ByOrg:       orgRows(rev.PerCustomer, byOrg),
		GeneratedAt: at,
	}
}

// grantTotals is the grant fold: fleet totals split by bucket, plus the row count.
type grantTotals struct {
	total, trial, prepaid money.Cents
	count                 int
}

// grantTally is one org's granted position.
type grantTally struct {
	cents money.Cents
	count int
}

// foldGrants sums the grant ledger fleet-wide and per TARGET org (GrantRow.Org is the
// org the credit landed on, not the staff actor's).
func foldGrants(rows []customer.GrantRow) (grantTotals, map[string]grantTally) {
	var t grantTotals
	byOrg := make(map[string]grantTally, len(rows))
	for _, g := range rows {
		amount := money.Cents(g.AmountCents)
		t.total += amount
		t.count++
		if g.Source == "prepaid" {
			t.prepaid += amount
		} else {
			t.trial += amount // legacy/absent source is a comp — the same rule the ledger uses
		}
		tally := byOrg[g.Org]
		tally.cents += amount
		tally.count++
		byOrg[g.Org] = tally
	}
	return t, byOrg
}

// orgRows joins the per-customer revenue rows with their grant tallies, biggest spender
// first. The revenue rows are the row SET: an org that was granted credit but no longer
// exists in IAM is counted in the fleet total and simply has no line of its own.
func orgRows(customers []revenue.RevenueCustomer, grants map[string]grantTally) []moneyOrgRow {
	out := make([]moneyOrgRow, 0, len(customers))
	for _, cst := range customers {
		g := grants[cst.Org]
		out = append(out, moneyOrgRow{
			Org:          cst.Org,
			Display:      cst.Display,
			Plan:         cst.Plan,
			SpendCents:   money.Cents(cst.SpendCents),
			BalanceCents: money.Cents(cst.BalanceCents),
			MRRCents:     money.Cents(cst.MRRCents),
			GrantedCents: g.cents,
			Grants:       g.count,
		})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SpendCents > out[j].SpendCents })
	return out
}

// mergeSources folds a domain's freshness rows in under a domain-qualified name, so two
// upstreams that share a bare name (revenue and finance both read "commerce") stay
// distinguishable instead of one silently shadowing the other.
func mergeSources(dst []core.SourceStatus, domain string, src []core.SourceStatus) []core.SourceStatus {
	for _, s := range src {
		s.Name = domain + ":" + s.Name
		dst = append(dst, s)
	}
	return dst
}
