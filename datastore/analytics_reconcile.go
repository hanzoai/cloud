// Copyright © 2026 Hanzo AI. MIT License.

package datastore

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"
)

// analytics_reconcile.go — EXACT INTEGER MINOR-UNIT FINANCIAL RECONCILIATION.
//
// Compares authoritative commerce org balances against analytical ClickHouse snapshots
// down to the single minor currency unit (cent). Opposite-sign drifts across distinct
// orgs are accumulated by absolute value so +100 on org A and -100 on org B never cancel
// out to a false zero.

// OrgDrift records the exact minor-unit discrepancy for one org balance.
type OrgDrift struct {
	Org              string
	Currency         string
	AuthCashMinor    int64
	AnalCashMinor    int64
	CashDriftMinor   int64 // analytical - authoritative
	AuthCreditMinor  int64
	AnalCreditMinor  int64
	CreditDriftMinor int64 // analytical - authoritative
	MissingInAnal    bool
	ExtraInAnal      bool
}

// ReconcileReport summarizes an exact financial reconciliation pass across orgs.
type ReconcileReport struct {
	CheckedOrgs           int
	MismatchOrgs          int
	TotalCashDriftMinor   int64 // sum of |CashDriftMinor| across all mismatched orgs
	TotalCreditDriftMinor int64 // sum of |CreditDriftMinor| across all mismatched orgs
	Exact                 bool  // true iff MismatchOrgs == 0
	Mismatches            []OrgDrift
	CheckedAt             time.Time
}

// ReconcileOrgBalances compares authoritative commerce balances against analytical
// mirror balances using exact integer minor-unit arithmetic.
func ReconcileOrgBalances(authoritative, analytical map[string]OrgBalanceState) ReconcileReport {
	keys := make([]string, 0, max(len(authoritative), len(analytical)))
	seen := make(map[string]struct{}, len(authoritative)+len(analytical))
	for k := range authoritative {
		seen[k] = struct{}{}
		keys = append(keys, k)
	}
	for k := range analytical {
		if _, ok := seen[k]; !ok {
			seen[k] = struct{}{}
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)

	report := ReconcileReport{
		CheckedOrgs: len(keys),
		Exact:       true,
		CheckedAt:   time.Now().UTC(),
	}

	for _, k := range keys {
		auth, inAuth := authoritative[k]
		anal, inAnal := analytical[k]

		cashDrift := anal.CashMinor - auth.CashMinor
		creditDrift := anal.CreditMinor - auth.CreditMinor
		if cashDrift == 0 && creditDrift == 0 {
			continue
		}

		org := auth.Org
		if org == "" {
			org = anal.Org
		}
		if org == "" {
			org = k
		}
		cur := auth.Currency
		if cur == "" {
			cur = anal.Currency
		}
		if cur == "" {
			cur = "usd"
		}

		report.MismatchOrgs++
		report.TotalCashDriftMinor += absInt64(cashDrift)
		report.TotalCreditDriftMinor += absInt64(creditDrift)
		report.Mismatches = append(report.Mismatches, OrgDrift{
			Org:              org,
			Currency:         cur,
			AuthCashMinor:    auth.CashMinor,
			AnalCashMinor:    anal.CashMinor,
			CashDriftMinor:   cashDrift,
			AuthCreditMinor:  auth.CreditMinor,
			AnalCreditMinor:  anal.CreditMinor,
			CreditDriftMinor: creditDrift,
			MissingInAnal:    inAuth && !inAnal,
			ExtraInAnal:      !inAuth && inAnal,
		})
	}

	report.Exact = report.MismatchOrgs == 0
	return report
}

// RecordReconcileWatermark persists the outcome of a financial reconciliation pass
// into `analytics.watermarks` for alerting and SLA dashboards.
func RecordReconcileWatermark(ctx context.Context, report ReconcileReport) error {
	checkedAt := report.CheckedAt
	if checkedAt.IsZero() {
		checkedAt = time.Now().UTC()
	}
	status := "exact"
	if !report.Exact {
		status = "drift"
	}
	return UpsertWatermark(ctx, Watermark{
		Pipeline:            "reconcile_org_balances",
		Source:              "commerce",
		WatermarkTS:         checkedAt.UTC(),
		LagMS:               0,
		RowsProcessed:       uint64(max(report.CheckedOrgs, 0)),
		ReconcileDriftMinor: report.TotalCashDriftMinor + report.TotalCreditDriftMinor,
		Status:              status,
		Detail: fmt.Sprintf(
			"checked=%d mismatches=%d cash_drift_minor=%d credit_drift_minor=%d",
			report.CheckedOrgs, report.MismatchOrgs, report.TotalCashDriftMinor, report.TotalCreditDriftMinor,
		),
		UpdatedAt: time.Now().UTC(),
	})
}

func absInt64(n int64) int64 {
	if n >= 0 {
		return n
	}
	if n == math.MinInt64 {
		return math.MaxInt64
	}
	return -n
}
