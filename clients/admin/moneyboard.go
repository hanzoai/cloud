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
	"github.com/hanzoai/cloud/clients/admin/commerce"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/admin/customer"
	"github.com/hanzoai/cloud/clients/admin/finance"
	"github.com/hanzoai/cloud/clients/admin/money"
	"github.com/hanzoai/cloud/clients/admin/revenue"
	"github.com/hanzoai/cloud/clients/treasury"
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
	Revenue     moneyRevenue        `json:"revenue"`
	Credits     moneyCredits        `json:"credits"`
	Infra       moneyInfra          `json:"infrastructure"`
	Margin      moneyMargin         `json:"margin"`
	ByOrg       []moneyOrgRow       `json:"byOrg"`
	GeneratedAt string              `json:"generatedAt"`
	Sources     []core.SourceStatus `json:"sources"`
}

// moneyRevenue is what customers actually pay us.
type moneyRevenue struct {
	RealizedCents money.Cents `json:"realizedCents"` // consumed spend, fleet-wide
	MRRCents      money.Cents `json:"mrrCents"`
	ARRCents      money.Cents `json:"arrCents"`
	ARPUCents     money.Cents `json:"arpuCents"`
	Customers     int         `json:"customers"`
	Paying        int         `json:"paying"`
}

// moneyCredits is the granted-vs-consumed ledger. Outstanding is the liability side:
// prepaid + promo balance customers still hold and have not yet burned.
type moneyCredits struct {
	GrantedCents        money.Cents `json:"grantedCents"`
	GrantedTrialCents   money.Cents `json:"grantedTrialCents"`   // non-cash comps/promos
	GrantedPrepaidCents money.Cents `json:"grantedPrepaidCents"` // real money added
	ConsumedCents       money.Cents `json:"consumedCents"`
	OutstandingCents    money.Cents `json:"outstandingCents"`
	Grants              int         `json:"grants"`
}

// moneyInfra is what the platform costs to run: vendor COGS plus the DigitalOcean
// burn-down, plus the platform reserve fund backing the payout programs.
type moneyInfra struct {
	Period                 string            `json:"period"`
	VendorCogsCents        money.Cents       `json:"vendorCogsCents"`
	DOMonthToDateCents     money.Cents       `json:"doMonthToDateCents"`
	DOCreditRemainingCents money.Cents       `json:"doCreditRemainingCents"`
	DOAvgDailyBurnCents    money.Cents       `json:"doAvgDailyBurnCents"`
	TreasuryReserveCents   money.Cents       `json:"treasuryReserveCents"`
	Vendors                []commerce.Vendor `json:"vendors"`
}

// moneyMargin is revenue against cost, carried through from finance so the two boards
// cannot report different margins.
type moneyMargin struct {
	GrossCents money.Cents `json:"grossCents"`
	GrossPct   float64     `json:"grossPct"`
	Profitable bool        `json:"profitable"`
	RunwayDays *float64    `json:"runwayDays"`
}

// moneyOrgRow is one customer's whole money position on a single line — what they were
// given, what they spent, and what they still hold.
type moneyOrgRow struct {
	Org          string      `json:"org"`
	Display      string      `json:"display"`
	Plan         string      `json:"plan"`
	SpendCents   money.Cents `json:"spendCents"`
	BalanceCents money.Cents `json:"balanceCents"`
	MRRCents     money.Cents `json:"mrrCents"`
	GrantedCents money.Cents `json:"grantedCents"`
	Grants       int         `json:"grants"`
}

// moneyBoardHandler answers GET /v1/admin/money.
func moneyBoardHandler(s *cloud.Service[core.State], c *zip.Ctx) error {
	ctx := c.Context()
	at := time.Now().UTC().Format(time.RFC3339)

	rev, revErr := revenue.Compute(s, ctx, core.CallerCreds(c))
	fin := finance.Compute(s, ctx, core.CallerCreds(c))
	grants, _, grantErr := customer.GrantRows(s, ctx, customer.GrantFilter("", "success", moneyGrantScan))

	board := foldMoney(rev, fin, grants, reserveCents(ctx), at)

	// Freshness, namespaced by the domain that read it.
	board.Sources = []core.SourceStatus{}
	if revErr != nil {
		board.Sources = append(board.Sources, core.SrcOf("revenue", revErr, 0, at))
	} else {
		board.Sources = mergeSources(board.Sources, "revenue", rev.Sources)
	}
	board.Sources = mergeSources(board.Sources, "finance", fin.Sources)
	board.Sources = append(board.Sources, grantSource(s, grants, grantErr, at))

	return core.OK(c, board)
}

// reserveCents reads the platform reserve fund. Not mounted ⇒ 0, which is the truth:
// there is no reserve on this deployment.
func reserveCents(ctx context.Context) money.Cents {
	cents, _ := treasury.ReserveCents(ctx)
	return money.Cents(cents)
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
