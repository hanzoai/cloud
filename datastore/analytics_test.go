// Copyright © 2026 Hanzo AI. MIT License.

package datastore

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var (
	reEngine  = regexp.MustCompile(`ENGINE\s*=\s*([A-Za-z0-9_]+)`)
	reMVTo    = regexp.MustCompile(`(?is)\bTO\s+([a-zA-Z0-9_]+\.[a-zA-Z0-9_]+)\s+AS\b`)
	reMVFrom  = regexp.MustCompile(`(?is)\bFROM\s+([a-zA-Z0-9_]+\.[a-zA-Z0-9_]+)\b`)
	reFloatTy = regexp.MustCompile(`\bFloat(32|64)\b`)
)

func TestAnalyticsSchemaDDLsAndInvariants(t *testing.T) {
	tables := AnalyticsTableDDLs()
	if len(tables) != 23 {
		t.Fatalf("AnalyticsTableDDLs len = %d, want 23", len(tables))
	}

	// Track table -> engine so MV invariant checks can verify source/target engines.
	engines := map[string]string{
		"hanzo.cloud_usage": "ReplacingMergeTree",
		"event.span":        "MergeTree",
		"event.fact":        "MergeTree",
	}

	financialTables := map[string]bool{
		AnalyticsFactCommerceLedgerTable:    true,
		AnalyticsStateOrgBalancesTable:      true,
		AnalyticsStateSubscriptionsTable:    true,
		AnalyticsStateInvoicesTable:         true,
		AnalyticsRollupCommerceDailyTable:   true,
		AnalyticsServingGlobalOverviewTable: true,
		AnalyticsServingOrgOverviewTable:    true,
	}

	tableSQL := make(map[string]string, len(tables))
	for _, tbl := range tables {
		tableSQL[tbl.Name] = tbl.SQL
		if !strings.HasPrefix(tbl.Name, "analytics.") {
			t.Errorf("table %q must reside in analytics namespace", tbl.Name)
		}
		if !strings.Contains(tbl.SQL, "CREATE TABLE IF NOT EXISTS "+tbl.Name) {
			t.Errorf("table %s DDL missing CREATE TABLE IF NOT EXISTS", tbl.Name)
		}
		if !strings.Contains(tbl.SQL, "ORDER BY (") {
			t.Errorf("table %s DDL missing ORDER BY clause", tbl.Name)
		}
		m := reEngine.FindStringSubmatch(tbl.SQL)
		if len(m) < 2 {
			t.Errorf("table %s DDL missing ENGINE clause", tbl.Name)
			continue
		}
		engines[tbl.Name] = m[1]

		if financialTables[tbl.Name] && reFloatTy.MatchString(tbl.SQL) {
			t.Errorf("financial table %s must NEVER use Float32/Float64 columns", tbl.Name)
		}
	}

	// Verify L1 1-minute real-time rollups have AggregatingMergeTree + 48h TTL.
	for _, mTbl := range []string{AnalyticsRollupAIMinuteTable, AnalyticsRollupO11yMinuteTable} {
		if engines[mTbl] != "AggregatingMergeTree" {
			t.Fatalf("%s engine = %q, want AggregatingMergeTree", mTbl, engines[mTbl])
		}
		if !strings.Contains(tableSQL[mTbl], "TTL minute + INTERVAL 48 HOUR") {
			t.Fatalf("%s missing 48h TTL clause", mTbl)
		}
	}

	// Verify L3 materialized serving snapshot tables use ReplacingMergeTree(refreshed_at)
	// and live-benchmarked index_granularity settings.
	for _, sTbl := range []string{
		AnalyticsServingGlobalOverviewTable,
		AnalyticsServingOrgOverviewTable,
		AnalyticsServingModelPerformanceTable,
		AnalyticsServingProviderEconomicsTable,
	} {
		if engines[sTbl] != "ReplacingMergeTree" {
			t.Fatalf("%s engine = %q, want ReplacingMergeTree", sTbl, engines[sTbl])
		}
		if !strings.Contains(tableSQL[sTbl], "ReplacingMergeTree(refreshed_at)") {
			t.Fatalf("%s must use ReplacingMergeTree(refreshed_at)", sTbl)
		}
	}
	if !strings.Contains(tableSQL[AnalyticsServingGlobalOverviewTable], "SETTINGS index_granularity = 64") {
		t.Fatal("serving_global_overview missing SETTINGS index_granularity = 64")
	}
	if !strings.Contains(tableSQL[AnalyticsServingOrgOverviewTable], "SETTINGS index_granularity = 256") {
		t.Fatal("serving_org_overview missing SETTINGS index_granularity = 256")
	}

	// Verify immutable flow fact vs latest-state engine separation.
	if engines[AnalyticsFactCommerceLedgerTable] != "MergeTree" {
		t.Fatalf("fact_commerce_ledger engine = %q, want immutable MergeTree", engines[AnalyticsFactCommerceLedgerTable])
	}
	for _, st := range []string{AnalyticsStateOrgBalancesTable, AnalyticsStateSubscriptionsTable, AnalyticsStateInvoicesTable} {
		if engines[st] != "ReplacingMergeTree" {
			t.Fatalf("%s engine = %q, want ReplacingMergeTree", st, engines[st])
		}
	}

	// Verify every MV is well-formed and NEVER feeds a ReplacingMergeTree into a SummingMergeTree.
	mvs := AnalyticsMVDDLs()
	if len(mvs) != 9 {
		t.Fatalf("AnalyticsMVDDLs len = %d, want 9", len(mvs))
	}
	mvSQL := make(map[string]string, len(mvs))
	for _, mv := range mvs {
		mvSQL[mv.Name] = mv.SQL
		if !strings.Contains(mv.SQL, "CREATE MATERIALIZED VIEW IF NOT EXISTS "+mv.Name) {
			t.Errorf("MV %s missing CREATE MATERIALIZED VIEW IF NOT EXISTS", mv.Name)
		}
		toMatch := reMVTo.FindStringSubmatch(mv.SQL)
		fromMatch := reMVFrom.FindStringSubmatch(mv.SQL)
		if len(toMatch) < 2 || len(fromMatch) < 2 {
			t.Errorf("MV %s missing TO or FROM target/source", mv.Name)
			continue
		}
		target := toMatch[1]
		source := fromMatch[1]
		targetEngine, okT := engines[target]
		sourceEngine, okS := engines[source]
		if !okT || !okS {
			t.Errorf("MV %s references unknown engine for source=%s(%v) target=%s(%v)", mv.Name, source, okS, target, okT)
			continue
		}
		if sourceEngine == "ReplacingMergeTree" && targetEngine == "SummingMergeTree" {
			t.Fatalf("FORBIDDEN: MV %s feeds mutable %s (%s) into %s (%s)", mv.Name, source, sourceEngine, target, targetEngine)
		}
		if mv.Name == "analytics.rollup_commerce_daily_mv" {
			if source != AnalyticsFactCommerceLedgerTable || target != AnalyticsRollupCommerceDailyTable {
				t.Fatalf("rollup_commerce_daily_mv wired %s -> %s, want %s -> %s", source, target, AnalyticsFactCommerceLedgerTable, AnalyticsRollupCommerceDailyTable)
			}
			if !strings.Contains(mv.SQL, "toInt64(sign)") {
				t.Fatalf("rollup_commerce_daily_mv must multiply amounts by sign")
			}
		}
	}

	// Verify AI TTFT and session activation lean rollups use AggregatingMergeTree,
	// strict WHERE filters on event.span, and never shadow event.span columns in GROUP BY.
	for _, rTbl := range []string{AnalyticsRollupAITTFTHourlyTable, AnalyticsRollupSessionActivationHourlyTable} {
		if engines[rTbl] != "AggregatingMergeTree" {
			t.Fatalf("%s engine = %q, want AggregatingMergeTree", rTbl, engines[rTbl])
		}
	}
	aiMV := mvSQL["analytics.mv_ai_ttft_hourly"]
	if !strings.Contains(aiMV, "WHERE kind = 'client' AND mapContains(attributes, 'gen_ai.system')") {
		t.Fatalf("mv_ai_ttft_hourly must strictly filter gen_ai client spans; got %s", aiMV)
	}
	if !strings.Contains(aiMV, "hanzo.ai.ttft_ms") || !strings.Contains(aiMV, "_o11y.gen_ai.total_cost") {
		t.Fatalf("mv_ai_ttft_hourly must pre-aggregate TTFT and gen_ai cost; got %s", aiMV)
	}
	actMV := mvSQL["analytics.mv_session_activation_hourly"]
	if !strings.Contains(actMV, "WHERE name = 'hanzo.session.activate' AND attributes['hanzo.session.activation'] != ''") {
		t.Fatalf("mv_session_activation_hourly must strictly filter activation spans; got %s", actMV)
	}
	// ClickHouse 26.2 guard: event.span has a native `kind` column, so GROUP BY must
	// spell the attribute expression rather than shadowing `kind`.
	if idx := strings.Index(actMV, "GROUP BY"); idx >= 0 && strings.Contains(actMV[idx:], "kind") {
		t.Fatalf("mv_session_activation_hourly GROUP BY must not shadow event.span.kind; got %s", actMV[idx:])
	}

	// Verify all 14 serving views exist and never scan raw event.* tables.
	wantViews := []string{
		"analytics.global_overview",
		"analytics.global_finance",
		"analytics.global_usage",
		"analytics.global_compute",
		"analytics.global_health",
		"analytics.global_growth",
		"analytics.global_security",
		"analytics.org_overview",
		"analytics.org_finance",
		"analytics.org_usage",
		"analytics.org_compute",
		"analytics.model_performance",
		"analytics.provider_economics",
		"analytics.product_performance",
	}
	views := AnalyticsViewDDLs()
	if len(views) != len(wantViews) {
		t.Fatalf("AnalyticsViewDDLs len = %d, want %d", len(views), len(wantViews))
	}
	gotViews := make(map[string]string, len(views))
	for _, v := range views {
		gotViews[v.Name] = v.SQL
	}
	for _, name := range wantViews {
		sql, ok := gotViews[name]
		if !ok {
			t.Errorf("missing required serving view %s", name)
			continue
		}
		if !strings.Contains(sql, "CREATE OR REPLACE VIEW "+name+" AS") {
			t.Errorf("view %s must use CREATE OR REPLACE VIEW", name)
		}
		if strings.Contains(sql, "event.span") || strings.Contains(sql, "event.fact") || strings.Contains(sql, "hanzo.cloud_usage") {
			t.Errorf("serving view %s must read compact analytics rollups/states, never raw telemetry tables", name)
		}
	}
}

func TestCanonicalCatalogCoverageAndIntegrity(t *testing.T) {
	catalog := CanonicalCatalog()
	if len(catalog) == 0 {
		t.Fatal("CanonicalCatalog is empty")
	}

	validTypes := map[string]bool{
		"counter": true, "gauge": true, "ratio": true, "histogram": true, "currency": true,
	}
	validUnits := map[string]bool{
		"minor_usd": true, "nano_usd": true, "count": true, "tokens": true,
		"bytes": true, "ns": true, "ms": true, "bps": true, "ratio": true, "seats": true,
	}
	validAccuracy := map[string]bool{
		"exact": true, "approximate": true, "eventual": true,
	}

	seenMetrics := make(map[string]bool, len(catalog))
	seenDomains := make(map[string]int)

	for _, m := range catalog {
		if m.Metric == "" {
			t.Error("catalog contains empty metric name")
		}
		if seenMetrics[m.Metric] {
			t.Errorf("duplicate metric in catalog: %s", m.Metric)
		}
		seenMetrics[m.Metric] = true
		seenDomains[m.Domain]++

		if !validTypes[m.Type] {
			t.Errorf("metric %s has invalid type %q", m.Metric, m.Type)
		}
		if !validUnits[m.Unit] {
			t.Errorf("metric %s has invalid unit %q", m.Metric, m.Unit)
		}
		if m.Source == "" {
			t.Errorf("metric %s missing source", m.Metric)
		}
		if m.Aggregation == "" {
			t.Errorf("metric %s missing aggregation", m.Metric)
		}
		if !strings.HasPrefix(m.ServingTable, "analytics.") {
			t.Errorf("metric %s serving table %q must be in analytics namespace", m.Metric, m.ServingTable)
		}
		if m.FreshnessTargetMS == 0 {
			t.Errorf("metric %s must define FreshnessTargetMS > 0", m.Metric)
		}
		if !validAccuracy[m.Accuracy] {
			t.Errorf("metric %s has invalid accuracy %q", m.Metric, m.Accuracy)
		}
		if m.Description == "" {
			t.Errorf("metric %s missing description", m.Metric)
		}
		if m.Type == "currency" || strings.HasSuffix(m.Metric, "_minor") || strings.HasSuffix(m.Metric, "_nano") {
			if m.Unit != "minor_usd" && m.Unit != "nano_usd" {
				t.Errorf("monetary metric %s must use minor_usd or nano_usd, got %q", m.Metric, m.Unit)
			}
			if m.Accuracy != "exact" {
				t.Errorf("monetary metric %s must have exact accuracy, got %q", m.Metric, m.Accuracy)
			}
		}
	}

	// All 20 Hanzo domains must be covered.
	wantDomains := []string{
		"commerce", "finance", "iam", "ai", "enso", "kai", "cloud", "compute",
		"storage", "networking", "bots", "sandboxes", "research", "product",
		"marketing", "sales", "support", "security", "infrastructure", "developer",
	}
	if len(CanonicalDomains()) != 20 {
		t.Fatalf("CanonicalDomains len = %d, want 20", len(CanonicalDomains()))
	}
	for _, d := range wantDomains {
		if seenDomains[d] == 0 {
			t.Errorf("canonical catalog missing metrics for domain %q", d)
		}
	}

	// LookupMetric & defensive copy check.
	got, ok := LookupMetric("commerce.mrr_minor")
	if !ok || got.Unit != "minor_usd" || got.Accuracy != "exact" {
		t.Fatalf("LookupMetric(commerce.mrr_minor) = (%+v, %v)", got, ok)
	}
	got.Dimensions[0] = "MUTATED"
	again, _ := LookupMetric("commerce.mrr_minor")
	if again.Dimensions[0] == "MUTATED" {
		t.Fatal("LookupMetric returned shared slice; expected defensive copy")
	}
	if _, ok := LookupMetric("nonexistent.metric"); ok {
		t.Fatal("LookupMetric unexpectedly found nonexistent.metric")
	}
}

func TestAnalyticsIngestBuildersAndValidation(t *testing.T) {
	now := time.Date(2026, 11, 25, 3, 0, 0, 0, time.UTC)

	// Commerce ledger fact insert
	fStmt, fArgs := buildCommerceLedgerInsert([]CommerceLedgerFact{
		{
			EntryID:     "e1",
			Org:         "acme",
			Product:     "ai",
			Kind:        "usage",
			Sign:        1,
			AmountMinor: 500,
			CashMinor:   500,
			Currency:    "usd",
			FXRateNano:  1_000_000_000,
		},
		{
			EntryID:     "e2",
			Org:         "acme",
			Product:     "ai",
			Kind:        "refund",
			Sign:        -1,
			AmountMinor: 100,
			CashMinor:   100,
		},
	}, now)
	if !strings.Contains(fStmt, AnalyticsFactCommerceLedgerTable) {
		t.Fatalf("ledger insert missing table: %s", fStmt)
	}
	if strings.Count(fStmt, "?") != 2*14 || len(fArgs) != 2*14 {
		t.Fatalf("ledger placeholders/args = %d/%d, want 28", strings.Count(fStmt, "?"), len(fArgs))
	}
	if fArgs[4] != int8(1) || fArgs[14+4] != int8(-1) || fArgs[14+8] != "usd" {
		t.Fatalf("unexpected ledger args: %#v", fArgs)
	}

	// Org balance states
	bStmt, bArgs := buildOrgBalanceStatesInsert([]OrgBalanceState{
		{Org: "acme", Currency: "usd", Books: true, CashMinor: 1000, CreditMinor: 250, LimitMinor: 5000, OwedMinor: 0, Version: 42},
	}, now)
	if !strings.Contains(bStmt, AnalyticsStateOrgBalancesTable) || strings.Count(bStmt, "?") != 9 || len(bArgs) != 9 {
		t.Fatalf("balance insert malformed: %d/%d", strings.Count(bStmt, "?"), len(bArgs))
	}
	if bArgs[2] != uint8(1) || bArgs[7] != uint64(42) {
		t.Fatalf("unexpected balance args: %#v", bArgs)
	}

	// Subscription states
	sStmt, sArgs := buildSubscriptionStatesInsert([]SubscriptionState{
		{Org: "acme", ID: "sub_1", Status: "active", MRRMinor: 2900, Seats: 3, Version: 7},
	}, now)
	if !strings.Contains(sStmt, AnalyticsStateSubscriptionsTable) || strings.Count(sStmt, "?") != 18 || len(sArgs) != 18 {
		t.Fatalf("subscription insert malformed: %d/%d", strings.Count(sStmt, "?"), len(sArgs))
	}

	// Invoice states
	iStmt, iArgs := buildInvoiceStatesInsert([]InvoiceState{
		{Org: "acme", ID: "inv_1", Status: "paid", AmountMinor: 2900, PaidMinor: 2900, Version: 9},
	}, now)
	if !strings.Contains(iStmt, AnalyticsStateInvoicesTable) || strings.Count(iStmt, "?") != 15 || len(iArgs) != 15 {
		t.Fatalf("invoice insert malformed: %d/%d", strings.Count(iStmt, "?"), len(iArgs))
	}

	// Dim orgs
	dStmt, dArgs := buildDimOrgsInsert([]DimOrg{
		{Org: "acme", DisplayName: "Acme Inc", IsInternal: false, Members: 12, Tier: "pro", Status: "active"},
	}, now)
	if !strings.Contains(dStmt, AnalyticsDimOrgsTable) || strings.Count(dStmt, "?") != 8 || len(dArgs) != 8 {
		t.Fatalf("dim_orgs insert malformed: %d/%d", strings.Count(dStmt, "?"), len(dArgs))
	}

	// Watermark
	wStmt, wArgs := buildWatermarkInsert(Watermark{
		Pipeline: "reconcile_org_balances", Source: "commerce", Status: "exact", RowsProcessed: 100,
	}, now)
	if !strings.Contains(wStmt, AnalyticsWatermarksTable) || strings.Count(wStmt, "?") != 9 || len(wArgs) != 9 {
		t.Fatalf("watermark insert malformed: %d/%d", strings.Count(wStmt, "?"), len(wArgs))
	}

	// Catalog insert
	cStmt, cArgs := buildCatalogInsert(CanonicalCatalog()[:3], now)
	if !strings.Contains(cStmt, AnalyticsCatalogTable) || strings.Count(cStmt, "?") != 3*12 || len(cArgs) != 3*12 {
		t.Fatalf("catalog insert malformed: %d/%d", strings.Count(cStmt, "?"), len(cArgs))
	}

	// Chunking verification across 450 rows -> 200, 200, 50
	many := make([]OrgBalanceState, 450)
	for i := range many {
		many[i] = OrgBalanceState{Org: fmt.Sprintf("org-%d", i), CashMinor: int64(i)}
	}
	var chunkSizes []int
	for start := 0; start < len(many); start += analyticsInsertChunk {
		end := min(start+analyticsInsertChunk, len(many))
		stmt, args := buildOrgBalanceStatesInsert(many[start:end], now)
		if len(args) != (end-start)*9 || strings.Count(stmt, "?") != len(args) {
			t.Fatalf("chunk [%d:%d] args=%d placeholders=%d", start, end, len(args), strings.Count(stmt, "?"))
		}
		chunkSizes = append(chunkSizes, end-start)
	}
	if len(chunkSizes) != 3 || chunkSizes[0] != 200 || chunkSizes[1] != 200 || chunkSizes[2] != 50 {
		t.Fatalf("chunk sizes = %v, want [200 200 50]", chunkSizes)
	}

	// Validation errors
	if err := validateCommerceLedgerFact(CommerceLedgerFact{Org: "acme", Sign: 1}); err == nil {
		t.Fatal("expected error for missing EntryID")
	}
	if err := validateCommerceLedgerFact(CommerceLedgerFact{EntryID: "e1", Sign: 1}); err == nil {
		t.Fatal("expected error for missing Org")
	}
	if err := validateCommerceLedgerFact(CommerceLedgerFact{EntryID: "e1", Org: "acme", Sign: 0}); err == nil {
		t.Fatal("expected error for Sign=0")
	}
	if err := validateCommerceLedgerFact(CommerceLedgerFact{EntryID: "e1", Org: "acme", Sign: 2}); err == nil {
		t.Fatal("expected error for Sign=2")
	}
}

func TestLedgerReplayDeduper(t *testing.T) {
	d := newLedgerDeduper(3)

	batch := []CommerceLedgerFact{
		{EntryID: "e1", Org: "acme", Sign: 1, AmountMinor: 100},
		{EntryID: "e1", Org: "acme", Sign: 1, AmountMinor: 100}, // intra-batch duplicate
		{EntryID: "e2", Org: "acme", Sign: 1, AmountMinor: 200},
	}

	// First filter removes intra-batch duplicate.
	filtered := d.filter(batch)
	if len(filtered) != 2 || filtered[0].EntryID != "e1" || filtered[1].EntryID != "e2" {
		t.Fatalf("intra-batch dedup got %+v", filtered)
	}

	// Before commit (simulating failed Exec), retry MUST still pass both rows!
	retry := d.filter(batch)
	if len(retry) != 2 {
		t.Fatalf("uncommitted retry suppressed rows: got %d, want 2", len(retry))
	}

	// After commit, replay MUST suppress already-committed entries.
	d.commit(filtered)
	replay := d.filter(batch)
	if len(replay) != 0 {
		t.Fatalf("committed replay allowed %d duplicates, want 0", len(replay))
	}

	// Ring eviction at capacity 3: committing e3 and e4 evicts e1.
	more := []CommerceLedgerFact{
		{EntryID: "e3", Org: "acme", Sign: 1},
		{EntryID: "e4", Org: "acme", Sign: 1},
	}
	d.commit(d.filter(more))
	if got := d.filter([]CommerceLedgerFact{{EntryID: "e2", Org: "acme", Sign: 1}}); len(got) != 0 {
		t.Fatalf("e2 should still be in ring, got %d", len(got))
	}
	if got := d.filter([]CommerceLedgerFact{{EntryID: "e1", Org: "acme", Sign: 1}}); len(got) != 1 {
		t.Fatalf("e1 should have been evicted from ring of cap 3, got %d", len(got))
	}

	// Concurrency safety under -race.
	cd := newLedgerDeduper(64)
	var wg sync.WaitGroup
	for g := range 8 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range 50 {
				rows := []CommerceLedgerFact{{EntryID: fmt.Sprintf("g%d-%d", id%2, i), Org: "acme", Sign: 1}}
				u := cd.filter(rows)
				cd.commit(u)
			}
		}(g)
	}
	wg.Wait()
}

func TestReconcileOrgBalances(t *testing.T) {
	t.Run("exact zero drift", func(t *testing.T) {
		auth := map[string]OrgBalanceState{
			"acme": {Org: "acme", Currency: "usd", CashMinor: 12500, CreditMinor: 5000},
			"beta": {Org: "beta", Currency: "usd", CashMinor: 0, CreditMinor: 100},
			"zero": {Org: "zero", Currency: "usd", CashMinor: 0, CreditMinor: 0},
		}
		anal := map[string]OrgBalanceState{
			"acme": {Org: "acme", Currency: "usd", CashMinor: 12500, CreditMinor: 5000},
			"beta": {Org: "beta", Currency: "usd", CashMinor: 0, CreditMinor: 100},
		}
		rep := ReconcileOrgBalances(auth, anal)
		if !rep.Exact || rep.CheckedOrgs != 3 || rep.MismatchOrgs != 0 || rep.TotalCashDriftMinor != 0 || rep.TotalCreditDriftMinor != 0 || len(rep.Mismatches) != 0 {
			t.Fatalf("expected exact report, got %+v", rep)
		}
	})

	t.Run("1-cent cash drift caught", func(t *testing.T) {
		auth := map[string]OrgBalanceState{
			"acme": {Org: "acme", Currency: "usd", CashMinor: 12500, CreditMinor: 5000},
		}
		anal := map[string]OrgBalanceState{
			"acme": {Org: "acme", Currency: "usd", CashMinor: 12501, CreditMinor: 5000},
		}
		rep := ReconcileOrgBalances(auth, anal)
		if rep.Exact || rep.MismatchOrgs != 1 || rep.TotalCashDriftMinor != 1 || rep.TotalCreditDriftMinor != 0 {
			t.Fatalf("expected 1-cent cash drift, got %+v", rep)
		}
		if len(rep.Mismatches) != 1 || rep.Mismatches[0].CashDriftMinor != 1 || rep.Mismatches[0].Org != "acme" {
			t.Fatalf("unexpected mismatch detail: %+v", rep.Mismatches)
		}
	})

	t.Run("opposite sign drifts do not cancel and missing/extra orgs reported", func(t *testing.T) {
		auth := map[string]OrgBalanceState{
			"a-plus":  {Org: "a-plus", Currency: "usd", CashMinor: 1000, CreditMinor: 0},
			"b-minus": {Org: "b-minus", Currency: "usd", CashMinor: 1000, CreditMinor: 50},
			"c-miss":  {Org: "c-miss", Currency: "usd", CashMinor: 250, CreditMinor: 0},
		}
		anal := map[string]OrgBalanceState{
			"a-plus":  {Org: "a-plus", Currency: "usd", CashMinor: 1100, CreditMinor: 0},  // +100 cash
			"b-minus": {Org: "b-minus", Currency: "usd", CashMinor: 900, CreditMinor: 49}, // -100 cash, -1 credit
			"d-extra": {Org: "d-extra", Currency: "usd", CashMinor: 0, CreditMinor: 75},   // +75 credit extra
		}
		rep := ReconcileOrgBalances(auth, anal)
		if rep.Exact {
			t.Fatalf("expected drift, got exact: %+v", rep)
		}
		if rep.CheckedOrgs != 4 || rep.MismatchOrgs != 4 {
			t.Fatalf("checked=%d mismatches=%d, want 4/4", rep.CheckedOrgs, rep.MismatchOrgs)
		}
		// |+100| + |-100| + |-250| = 450 cash drift; |-1| + |+75| = 76 credit drift
		if rep.TotalCashDriftMinor != 450 || rep.TotalCreditDriftMinor != 76 {
			t.Fatalf("drifts cash=%d credit=%d, want 450/76", rep.TotalCashDriftMinor, rep.TotalCreditDriftMinor)
		}
		if !rep.Mismatches[2].MissingInAnal || rep.Mismatches[2].Org != "c-miss" {
			t.Fatalf("expected c-miss MissingInAnal, got %+v", rep.Mismatches[2])
		}
		if !rep.Mismatches[3].ExtraInAnal || rep.Mismatches[3].Org != "d-extra" {
			t.Fatalf("expected d-extra ExtraInAnal, got %+v", rep.Mismatches[3])
		}
	})
}

func TestDimUsersAndMonotonicVersion(t *testing.T) {
	now := time.Date(2026, 11, 25, 3, 0, 0, 0, time.UTC)
	uStmt, uArgs := buildDimUsersInsert([]DimUser{
		{Org: "acme", UserID: "u1", Name: "alice", Email: "alice@acme.io", IsAdmin: true, Forbidden: false},
		{Org: "acme", UserID: "u2", Name: "bob", Email: "bob@acme.io", IsAdmin: false, Forbidden: true},
	}, now)
	if !strings.Contains(uStmt, AnalyticsDimUsersTable) || strings.Count(uStmt, "?") != 2*9 || len(uArgs) != 2*9 {
		t.Fatalf("dim_users insert malformed: placeholders=%d args=%d", strings.Count(uStmt, "?"), len(uArgs))
	}
	if uArgs[6] != uint8(1) || uArgs[7] != uint8(0) || uArgs[9+6] != uint8(0) || uArgs[9+7] != uint8(1) {
		t.Fatalf("unexpected dim_users flags: %#v", uArgs)
	}

	// NextAnalyticsVersion must be strictly unique and monotonic under concurrency.
	const workers = 8
	const perWorker = 100
	ch := make(chan uint64, workers*perWorker)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var prev uint64
			for range perWorker {
				v := NextAnalyticsVersion()
				if v <= prev {
					t.Errorf("non-monotonic version: %d <= %d", v, prev)
				}
				prev = v
				ch <- v
			}
		}()
	}
	wg.Wait()
	close(ch)
	seen := make(map[uint64]bool, workers*perWorker)
	for v := range ch {
		if seen[v] {
			t.Fatalf("duplicate version generated: %d", v)
		}
		seen[v] = true
	}
}

func TestSnapshotSQLBuildersAndRefresherLoop(t *testing.T) {
	now := time.Date(2026, 11, 25, 3, 30, 0, 0, time.UTC)

	gStmt, gArgs := buildGlobalOverviewSnapshotInsert(now)
	if !strings.Contains(gStmt, AnalyticsServingGlobalOverviewTable) || !strings.Contains(gStmt, "FROM analytics.global_overview") {
		t.Fatalf("global snapshot SQL unexpected: %s", gStmt)
	}
	if strings.Contains(gStmt, "event.") || strings.Contains(gStmt, "hanzo.cloud_usage") {
		t.Fatal("global snapshot SQL must never scan raw event.* or cloud_usage")
	}
	if strings.Count(gStmt, "?") != 1 || len(gArgs) != 1 {
		t.Fatalf("global snapshot args=%d placeholders=%d, want 1", len(gArgs), strings.Count(gStmt, "?"))
	}

	mStmt, mArgs := buildModelPerformanceSnapshotInsert(now)
	if !strings.Contains(mStmt, AnalyticsServingModelPerformanceTable) || len(mArgs) != 1 {
		t.Fatalf("model performance snapshot SQL unexpected")
	}
	pStmt, pArgs := buildProviderEconomicsSnapshotInsert(now)
	if !strings.Contains(pStmt, AnalyticsServingProviderEconomicsTable) || len(pArgs) != 1 {
		t.Fatalf("provider economics snapshot SQL unexpected")
	}

	// Incremental org snapshot pushes WHERE org IN (?,?) into all 4 L1/L2 branches.
	oStmt, oArgs := buildOrgOverviewSnapshotInsert([]string{"acme", "globex"}, now)
	if !strings.Contains(oStmt, AnalyticsServingOrgOverviewTable) {
		t.Fatalf("org snapshot missing target table")
	}
	if strings.Count(oStmt, "WHERE org IN (?,?)") != 3 || !strings.Contains(oStmt, "AND org IN (?,?)") {
		t.Fatalf("org snapshot must push org predicate into all 4 branches: %s", oStmt)
	}
	if strings.Count(oStmt, "?") != 1+4*2 || len(oArgs) != 1+4*2 {
		t.Fatalf("org snapshot placeholders=%d args=%d, want 9", strings.Count(oStmt, "?"), len(oArgs))
	}

	// Dirty org queue deduplicates, caps, and drains in sorted batches.
	q := newDirtyOrgQueue(4)
	q.mark("zeta", "acme", "acme", "", "beta", "gamma", "overflow")
	if q.pending() != 4 {
		t.Fatalf("pending = %d, want 4 (capped)", q.pending())
	}
	b1 := q.drain(2)
	b2 := q.drain(10)
	if len(b1) != 2 || len(b2) != 2 || q.pending() != 0 {
		t.Fatalf("unexpected batches: b1=%v b2=%v pending=%d", b1, b2, q.pending())
	}

	// Continuous refresher loop: verifies boot refresh, periodic ticks, debounced dirty orgs, and clean exit.
	ctx, cancel := context.WithCancel(context.Background())
	var globalHits, modelHits atomic.Int32
	var mu sync.Mutex
	var refreshedOrgs []string
	orgDone := make(chan struct{}, 4)

	loopQ := newDirtyOrgQueue(64)
	exited := make(chan struct{})
	go runSnapshotRefresherLoop(ctx, snapshotRefresherConfig{
		globalInterval:        25 * time.Millisecond,
		modelProviderInterval: 40 * time.Millisecond,
		dirtyDebounce:         15 * time.Millisecond,
		dirtyBatchSize:        10,
		refreshTimeout:        time.Second,
		dirtyQueue:            loopQ,
		isReady:               func() bool { return true },
		refreshGlobal: func(context.Context) error {
			globalHits.Add(1)
			return nil
		},
		refreshModelProvider: func(context.Context) error {
			modelHits.Add(1)
			return nil
		},
		refreshOrgs: func(_ context.Context, orgs []string) error {
			mu.Lock()
			refreshedOrgs = append(refreshedOrgs, orgs...)
			mu.Unlock()
			select {
			case orgDone <- struct{}{}:
			default:
			}
			return nil
		},
	}, func() { close(exited) })

	// Burst duplicate mutations for acme + beta; debounce should coalesce into one batch.
	loopQ.mark("acme", "acme", "beta", "acme")
	select {
	case <-orgDone:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for debounced dirty org refresh")
	}

	time.Sleep(60 * time.Millisecond)
	cancel()
	select {
	case <-exited:
	case <-time.After(time.Second):
		t.Fatal("snapshot refresher did not exit on ctx cancel")
	}

	if globalHits.Load() < 2 || modelHits.Load() < 2 {
		t.Fatalf("expected >=2 ticks, got global=%d model=%d", globalHits.Load(), modelHits.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(refreshedOrgs) != 2 || refreshedOrgs[0] != "acme" || refreshedOrgs[1] != "beta" {
		t.Fatalf("refreshedOrgs = %v, want [acme beta]", refreshedOrgs)
	}
}
