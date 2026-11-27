// Copyright © 2026 Hanzo AI. MIT License.

package datastore

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

// analytics_catalog.go — CANONICAL METRIC CATALOG ACROSS ALL 20 HANZO DOMAINS.
//
// Every metric served by any Hanzo board, API, or agent has one canonical definition:
// its domain, unit (integer minor_usd / nano_usd for money — never float dollars),
// authoritative source, aggregation semantics, serving view, freshness SLO, and accuracy
// contract ("exact" for all financial/billing figures).

// MetricDef defines one canonical metric in `analytics.catalog`.
type MetricDef struct {
	Metric            string
	Domain            string
	Type              string
	Unit              string
	Source            string
	Aggregation       string
	Dimensions        []string
	ServingTable      string
	FreshnessTargetMS uint32
	Accuracy          string
	Description       string
}

// CanonicalDomains lists all 20 Hanzo analytical domains covered by CanonicalCatalog.
var canonicalDomains = []string{
	"commerce",
	"finance",
	"iam",
	"ai",
	"enso",
	"kai",
	"cloud",
	"compute",
	"storage",
	"networking",
	"bots",
	"sandboxes",
	"research",
	"product",
	"marketing",
	"sales",
	"support",
	"security",
	"infrastructure",
	"developer",
}

// CanonicalDomains returns a copy of all 20 canonical Hanzo analytical domains.
func CanonicalDomains() []string {
	return slices.Clone(canonicalDomains)
}

var canonicalMetrics = []MetricDef{
	// 1. Commerce
	{
		Metric:            "commerce.mrr_minor",
		Domain:            "commerce",
		Type:              "currency",
		Unit:              "minor_usd",
		Source:            AnalyticsStateSubscriptionsTable,
		Aggregation:       "sum",
		Dimensions:        []string{"org", "currency", "plan_id", "provider"},
		ServingTable:      "analytics.global_finance",
		FreshnessTargetMS: 15000,
		Accuracy:          "exact",
		Description:       "Active non-test recurring subscription revenue normalized to monthly minor currency units.",
	},
	{
		Metric:            "commerce.active_subscriptions",
		Domain:            "commerce",
		Type:              "gauge",
		Unit:              "count",
		Source:            AnalyticsStateSubscriptionsTable,
		Aggregation:       "count",
		Dimensions:        []string{"org", "plan_id", "status", "provider"},
		ServingTable:      "analytics.global_overview",
		FreshnessTargetMS: 15000,
		Accuracy:          "exact",
		Description:       "Count of active or trialing non-deleted, non-test subscriptions.",
	},
	{
		Metric:            "commerce.gmv_minor",
		Domain:            "commerce",
		Type:              "currency",
		Unit:              "minor_usd",
		Source:            AnalyticsFactCommerceLedgerTable,
		Aggregation:       "sum",
		Dimensions:        []string{"day", "org", "product", "currency"},
		ServingTable:      "analytics.product_performance",
		FreshnessTargetMS: 15000,
		Accuracy:          "exact",
		Description:       "Gross signed transaction volume across usage and paid invoices in integer minor currency units.",
	},
	{
		Metric:            "commerce.invoices_paid_minor",
		Domain:            "commerce",
		Type:              "currency",
		Unit:              "minor_usd",
		Source:            AnalyticsStateInvoicesTable,
		Aggregation:       "sum",
		Dimensions:        []string{"org", "currency", "status"},
		ServingTable:      "analytics.org_finance",
		FreshnessTargetMS: 15000,
		Accuracy:          "exact",
		Description:       "Total settled invoice amount in integer minor currency units.",
	},

	// 2. Finance
	{
		Metric:            "finance.cash_balance_minor",
		Domain:            "finance",
		Type:              "currency",
		Unit:              "minor_usd",
		Source:            AnalyticsStateOrgBalancesTable,
		Aggregation:       "sum",
		Dimensions:        []string{"org", "currency"},
		ServingTable:      "analytics.global_finance",
		FreshnessTargetMS: 15000,
		Accuracy:          "exact",
		Description:       "Unspent deposited customer cash held across org ledgers in integer minor units.",
	},
	{
		Metric:            "finance.credit_balance_minor",
		Domain:            "finance",
		Type:              "currency",
		Unit:              "minor_usd",
		Source:            AnalyticsStateOrgBalancesTable,
		Aggregation:       "sum",
		Dimensions:        []string{"org", "currency"},
		ServingTable:      "analytics.global_finance",
		FreshnessTargetMS: 15000,
		Accuracy:          "exact",
		Description:       "Unspent granted promotional/enterprise credits across org ledgers in integer minor units.",
	},
	{
		Metric:            "finance.owed_balance_minor",
		Domain:            "finance",
		Type:              "currency",
		Unit:              "minor_usd",
		Source:            AnalyticsStateOrgBalancesTable,
		Aggregation:       "sum",
		Dimensions:        []string{"org", "currency"},
		ServingTable:      "analytics.org_finance",
		FreshnessTargetMS: 15000,
		Accuracy:          "exact",
		Description:       "Outstanding post-paid balance owed by orgs in integer minor units.",
	},
	{
		Metric:            "finance.gross_margin_nano",
		Domain:            "finance",
		Type:              "currency",
		Unit:              "nano_usd",
		Source:            "hanzo.cloud_usage",
		Aggregation:       "sum",
		Dimensions:        []string{"day", "org", "provider", "model"},
		ServingTable:      "analytics.provider_economics",
		FreshnessTargetMS: 30000,
		Accuracy:          "exact",
		Description:       "Billed revenue minus provider COGS in integer nano-USD (sum first, round once).",
	},
	{
		Metric:            "finance.reconcile_drift_minor",
		Domain:            "finance",
		Type:              "currency",
		Unit:              "minor_usd",
		Source:            AnalyticsWatermarksTable,
		Aggregation:       "latest",
		Dimensions:        []string{"pipeline"},
		ServingTable:      AnalyticsWatermarksTable,
		FreshnessTargetMS: 60000,
		Accuracy:          "exact",
		Description:       "Absolute integer minor-unit discrepancy between authoritative commerce books and analytical state.",
	},

	// 3. IAM
	{
		Metric:            "iam.orgs_total",
		Domain:            "iam",
		Type:              "gauge",
		Unit:              "count",
		Source:            AnalyticsDimOrgsTable,
		Aggregation:       "count",
		Dimensions:        []string{"is_internal", "tier", "status"},
		ServingTable:      "analytics.global_overview",
		FreshnessTargetMS: 60000,
		Accuracy:          "exact",
		Description:       "Total registered organizations in the Hanzo IAM directory.",
	},
	{
		Metric:            "iam.users_total",
		Domain:            "iam",
		Type:              "gauge",
		Unit:              "count",
		Source:            AnalyticsDimUsersTable,
		Aggregation:       "count",
		Dimensions:        []string{"org", "is_admin", "forbidden"},
		ServingTable:      "analytics.global_security",
		FreshnessTargetMS: 60000,
		Accuracy:          "exact",
		Description:       "Total user accounts provisioned across organizations.",
	},
	{
		Metric:            "iam.active_users_7d",
		Domain:            "iam",
		Type:              "gauge",
		Unit:              "count",
		Source:            AnalyticsDimUsersTable,
		Aggregation:       "count",
		Dimensions:        []string{"org"},
		ServingTable:      "analytics.global_security",
		FreshnessTargetMS: 60000,
		Accuracy:          "exact",
		Description:       "Users with a recorded sign-in within the trailing 7 days.",
	},

	// 4. AI
	{
		Metric:            "ai.requests",
		Domain:            "ai",
		Type:              "counter",
		Unit:              "count",
		Source:            "hanzo.cloud_usage",
		Aggregation:       "count",
		Dimensions:        []string{"hour", "day", "org", "model", "provider"},
		ServingTable:      "analytics.global_usage",
		FreshnessTargetMS: 10000,
		Accuracy:          "exact",
		Description:       "Total AI router inference requests served.",
	},
	{
		Metric:            "ai.tokens_total",
		Domain:            "ai",
		Type:              "counter",
		Unit:              "tokens",
		Source:            "hanzo.cloud_usage",
		Aggregation:       "sum",
		Dimensions:        []string{"hour", "day", "org", "model", "provider"},
		ServingTable:      "analytics.model_performance",
		FreshnessTargetMS: 10000,
		Accuracy:          "exact",
		Description:       "Total prompt + completion tokens processed across AI models.",
	},
	{
		Metric:            "ai.cost_nano",
		Domain:            "ai",
		Type:              "currency",
		Unit:              "nano_usd",
		Source:            "hanzo.cloud_usage",
		Aggregation:       "sum",
		Dimensions:        []string{"hour", "day", "org", "model", "provider"},
		ServingTable:      "analytics.provider_economics",
		FreshnessTargetMS: 10000,
		Accuracy:          "exact",
		Description:       "Upstream provider inference COGS in integer nano-USD.",
	},
	{
		Metric:            "ai.billed_nano",
		Domain:            "ai",
		Type:              "currency",
		Unit:              "nano_usd",
		Source:            "hanzo.cloud_usage",
		Aggregation:       "sum",
		Dimensions:        []string{"hour", "day", "org", "model", "provider"},
		ServingTable:      "analytics.org_usage",
		FreshnessTargetMS: 10000,
		Accuracy:          "exact",
		Description:       "Customer-billed AI inference spend in integer nano-USD.",
	},
	{
		Metric:            "ai.cache_saved_nano",
		Domain:            "ai",
		Type:              "currency",
		Unit:              "nano_usd",
		Source:            "hanzo.cloud_usage",
		Aggregation:       "sum",
		Dimensions:        []string{"day", "org", "model", "provider"},
		ServingTable:      "analytics.model_performance",
		FreshnessTargetMS: 30000,
		Accuracy:          "exact",
		Description:       "Prompt-cache savings credited on customer inference bills in integer nano-USD.",
	},
	{
		Metric:            "ai.error_rate",
		Domain:            "ai",
		Type:              "ratio",
		Unit:              "ratio",
		Source:            "hanzo.cloud_usage",
		Aggregation:       "ratio",
		Dimensions:        []string{"model", "provider"},
		ServingTable:      "analytics.model_performance",
		FreshnessTargetMS: 10000,
		Accuracy:          "exact",
		Description:       "Fraction of AI inference requests returning status='error'.",
	},

	// 5. Enso
	{
		Metric:            "enso.sessions",
		Domain:            "enso",
		Type:              "counter",
		Unit:              "count",
		Source:            "event.fact",
		Aggregation:       "uniq",
		Dimensions:        []string{"hour", "org"},
		ServingTable:      "analytics.product_performance",
		FreshnessTargetMS: 30000,
		Accuracy:          "approximate",
		Description:       "Distinct interactive Enso workspace sessions.",
	},
	{
		Metric:            "enso.events",
		Domain:            "enso",
		Type:              "counter",
		Unit:              "count",
		Source:            "event.fact",
		Aggregation:       "count",
		Dimensions:        []string{"hour", "org", "name"},
		ServingTable:      "analytics.product_performance",
		FreshnessTargetMS: 30000,
		Accuracy:          "exact",
		Description:       "Total Enso canvas and generation telemetry events.",
	},

	// 6. Kai
	{
		Metric:            "kai.tasks_executed",
		Domain:            "kai",
		Type:              "counter",
		Unit:              "count",
		Source:            "event.fact",
		Aggregation:       "count",
		Dimensions:        []string{"hour", "org", "name"},
		ServingTable:      "analytics.product_performance",
		FreshnessTargetMS: 30000,
		Accuracy:          "exact",
		Description:       "Total autonomous Kai assistant task executions.",
	},
	{
		Metric:            "kai.active_users",
		Domain:            "kai",
		Type:              "gauge",
		Unit:              "count",
		Source:            "event.fact",
		Aggregation:       "uniq",
		Dimensions:        []string{"hour", "org"},
		ServingTable:      "analytics.product_performance",
		FreshnessTargetMS: 60000,
		Accuracy:          "approximate",
		Description:       "Distinct active users interacting with Kai.",
	},

	// 7. Cloud
	{
		Metric:            "cloud.events",
		Domain:            "cloud",
		Type:              "counter",
		Unit:              "count",
		Source:            "event.fact",
		Aggregation:       "count",
		Dimensions:        []string{"hour", "org", "product", "name"},
		ServingTable:      "analytics.product_performance",
		FreshnessTargetMS: 30000,
		Accuracy:          "exact",
		Description:       "Total Hanzo Cloud control-plane and deployment events.",
	},
	{
		Metric:            "cloud.active_orgs",
		Domain:            "cloud",
		Type:              "gauge",
		Unit:              "count",
		Source:            AnalyticsRollupAIDailyTable,
		Aggregation:       "uniq",
		Dimensions:        []string{"day"},
		ServingTable:      "analytics.global_usage",
		FreshnessTargetMS: 60000,
		Accuracy:          "exact",
		Description:       "Distinct organizations active on Hanzo Cloud.",
	},

	// 8. Compute
	{
		Metric:            "compute.spans",
		Domain:            "compute",
		Type:              "counter",
		Unit:              "count",
		Source:            "event.span",
		Aggregation:       "count",
		Dimensions:        []string{"hour", "org", "service"},
		ServingTable:      "analytics.global_compute",
		FreshnessTargetMS: 15000,
		Accuracy:          "exact",
		Description:       "Total traced compute and workload execution spans.",
	},
	{
		Metric:            "compute.latency_p95_ns",
		Domain:            "compute",
		Type:              "histogram",
		Unit:              "ns",
		Source:            "event.span",
		Aggregation:       "quantile_p95",
		Dimensions:        []string{"hour", "org", "service"},
		ServingTable:      "analytics.org_compute",
		FreshnessTargetMS: 15000,
		Accuracy:          "approximate",
		Description:       "95th-percentile workload span duration in nanoseconds (t-digest).",
	},

	// 9. Storage
	{
		Metric:            "storage.bytes_ingested",
		Domain:            "storage",
		Type:              "counter",
		Unit:              "bytes",
		Source:            "event.fact",
		Aggregation:       "sum",
		Dimensions:        []string{"hour", "org", "product"},
		ServingTable:      "analytics.product_performance",
		FreshnessTargetMS: 30000,
		Accuracy:          "exact",
		Description:       "Total telemetry and storage payload bytes ingested.",
	},

	// 10. Networking
	{
		Metric:            "networking.service_errors",
		Domain:            "networking",
		Type:              "counter",
		Unit:              "count",
		Source:            "event.span",
		Aggregation:       "sum",
		Dimensions:        []string{"hour", "org", "service"},
		ServingTable:      "analytics.global_compute",
		FreshnessTargetMS: 15000,
		Accuracy:          "exact",
		Description:       "Total gateway, proxy, and RPC span errors across services.",
	},

	// 11. Bots
	{
		Metric:            "bots.events",
		Domain:            "bots",
		Type:              "counter",
		Unit:              "count",
		Source:            "event.fact",
		Aggregation:       "count",
		Dimensions:        []string{"hour", "org", "name"},
		ServingTable:      "analytics.product_performance",
		FreshnessTargetMS: 30000,
		Accuracy:          "exact",
		Description:       "Total Hanzo Bot messages and automation trigger events.",
	},

	// 12. Sandboxes
	{
		Metric:            "sandboxes.executions",
		Domain:            "sandboxes",
		Type:              "counter",
		Unit:              "count",
		Source:            "event.span",
		Aggregation:       "count",
		Dimensions:        []string{"hour", "org", "service"},
		ServingTable:      "analytics.org_compute",
		FreshnessTargetMS: 15000,
		Accuracy:          "exact",
		Description:       "Total ephemeral sandbox container/VM execution spans.",
	},

	// 13. Research
	{
		Metric:            "research.eval_events",
		Domain:            "research",
		Type:              "counter",
		Unit:              "count",
		Source:            "event.fact",
		Aggregation:       "count",
		Dimensions:        []string{"hour", "org", "name"},
		ServingTable:      "analytics.product_performance",
		FreshnessTargetMS: 60000,
		Accuracy:          "exact",
		Description:       "Evaluation runs, benchmark submissions, and research telemetry events.",
	},

	// 14. Product
	{
		Metric:            "product.events",
		Domain:            "product",
		Type:              "counter",
		Unit:              "count",
		Source:            "event.fact",
		Aggregation:       "count",
		Dimensions:        []string{"hour", "org", "product", "name"},
		ServingTable:      "analytics.product_performance",
		FreshnessTargetMS: 30000,
		Accuracy:          "exact",
		Description:       "Total product analytics events across all Hanzo surfaces.",
	},
	{
		Metric:            "product.dau",
		Domain:            "product",
		Type:              "gauge",
		Unit:              "count",
		Source:            "event.fact",
		Aggregation:       "uniq",
		Dimensions:        []string{"day", "product"},
		ServingTable:      "analytics.global_growth",
		FreshnessTargetMS: 60000,
		Accuracy:          "approximate",
		Description:       "Daily active distinct persons across Hanzo products.",
	},
	{
		Metric:            "product.sessions",
		Domain:            "product",
		Type:              "counter",
		Unit:              "count",
		Source:            "event.fact",
		Aggregation:       "uniq",
		Dimensions:        []string{"day", "product"},
		ServingTable:      "analytics.global_growth",
		FreshnessTargetMS: 60000,
		Accuracy:          "approximate",
		Description:       "Distinct user sessions across Hanzo products.",
	},

	// 15. Marketing
	{
		Metric:            "marketing.new_orgs",
		Domain:            "marketing",
		Type:              "counter",
		Unit:              "count",
		Source:            AnalyticsDimOrgsTable,
		Aggregation:       "count",
		Dimensions:        []string{"day", "is_internal"},
		ServingTable:      "analytics.global_growth",
		FreshnessTargetMS: 60000,
		Accuracy:          "exact",
		Description:       "Daily new organization signups acquired.",
	},

	// 16. Sales
	{
		Metric:            "sales.paying_orgs",
		Domain:            "sales",
		Type:              "gauge",
		Unit:              "count",
		Source:            AnalyticsStateOrgBalancesTable,
		Aggregation:       "count",
		Dimensions:        []string{"currency"},
		ServingTable:      "analytics.global_finance",
		FreshnessTargetMS: 30000,
		Accuracy:          "exact",
		Description:       "Organizations with active commerce books or paying subscriptions.",
	},

	// 17. Support
	{
		Metric:            "support.ticket_events",
		Domain:            "support",
		Type:              "counter",
		Unit:              "count",
		Source:            "event.fact",
		Aggregation:       "count",
		Dimensions:        []string{"hour", "org", "name"},
		ServingTable:      "analytics.product_performance",
		FreshnessTargetMS: 60000,
		Accuracy:          "exact",
		Description:       "Support ticket lifecycle events (opened, escalated, resolved).",
	},

	// 18. Security
	{
		Metric:            "security.forbidden_users",
		Domain:            "security",
		Type:              "gauge",
		Unit:              "count",
		Source:            AnalyticsDimUsersTable,
		Aggregation:       "count",
		Dimensions:        []string{"org"},
		ServingTable:      "analytics.global_security",
		FreshnessTargetMS: 30000,
		Accuracy:          "exact",
		Description:       "Suspended or forbidden user accounts across organizations.",
	},
	{
		Metric:            "security.admin_users",
		Domain:            "security",
		Type:              "gauge",
		Unit:              "count",
		Source:            AnalyticsDimUsersTable,
		Aggregation:       "count",
		Dimensions:        []string{"org"},
		ServingTable:      "analytics.global_security",
		FreshnessTargetMS: 30000,
		Accuracy:          "exact",
		Description:       "Privileged admin accounts per organization for least-privilege auditing.",
	},

	// 19. Infrastructure
	{
		Metric:            "infrastructure.service_error_rate",
		Domain:            "infrastructure",
		Type:              "ratio",
		Unit:              "ratio",
		Source:            "event.span",
		Aggregation:       "ratio",
		Dimensions:        []string{"service"},
		ServingTable:      "analytics.global_health",
		FreshnessTargetMS: 15000,
		Accuracy:          "exact",
		Description:       "Rolling 24h span error ratio per backend service.",
	},
	{
		Metric:            "infrastructure.service_latency_p99_ns",
		Domain:            "infrastructure",
		Type:              "histogram",
		Unit:              "ns",
		Source:            "event.span",
		Aggregation:       "quantile_p99",
		Dimensions:        []string{"service"},
		ServingTable:      "analytics.global_health",
		FreshnessTargetMS: 15000,
		Accuracy:          "approximate",
		Description:       "Rolling 24h 99th-percentile span latency per service in nanoseconds.",
	},

	// 20. Developer
	{
		Metric:            "developer.api_requests",
		Domain:            "developer",
		Type:              "counter",
		Unit:              "count",
		Source:            "hanzo.cloud_usage",
		Aggregation:       "count",
		Dimensions:        []string{"day", "org", "model"},
		ServingTable:      "analytics.org_usage",
		FreshnessTargetMS: 15000,
		Accuracy:          "exact",
		Description:       "Authenticated developer API calls across customer organizations.",
	},
	{
		Metric:            "developer.sdk_events",
		Domain:            "developer",
		Type:              "counter",
		Unit:              "count",
		Source:            "event.fact",
		Aggregation:       "count",
		Dimensions:        []string{"hour", "org", "product", "name"},
		ServingTable:      "analytics.product_performance",
		FreshnessTargetMS: 30000,
		Accuracy:          "exact",
		Description:       "CLI, SDK, and webhook developer telemetry events.",
	},
}

var (
	catalogIndexOnce sync.Once
	catalogIndex     map[string]MetricDef
)

func initCatalogIndex() {
	catalogIndexOnce.Do(func() {
		catalogIndex = make(map[string]MetricDef, len(canonicalMetrics))
		for _, m := range canonicalMetrics {
			catalogIndex[m.Metric] = m
		}
	})
}

func cloneMetricDef(m MetricDef) MetricDef {
	m.Dimensions = slices.Clone(m.Dimensions)
	return m
}

// CanonicalCatalog returns a defensive copy of all canonical Hanzo metric definitions.
func CanonicalCatalog() []MetricDef {
	out := make([]MetricDef, len(canonicalMetrics))
	for i, m := range canonicalMetrics {
		out[i] = cloneMetricDef(m)
	}
	return out
}

// LookupMetric returns the canonical metric definition by name, if registered.
func LookupMetric(name string) (MetricDef, bool) {
	initCatalogIndex()
	m, ok := catalogIndex[name]
	if !ok {
		return MetricDef{}, false
	}
	return cloneMetricDef(m), true
}

func buildCatalogInsert(rows []MetricDef, now time.Time) (string, []any) {
	const cols = 12
	vals := make([]string, 0, len(rows))
	args := make([]any, 0, len(rows)*cols)
	ts := now.UTC()
	for _, r := range rows {
		dims := r.Dimensions
		if dims == nil {
			dims = []string{}
		}
		vals = append(vals, "(?,?,?,?,?,?,?,?,?,?,?,?)")
		args = append(args,
			r.Metric, r.Domain, r.Type, r.Unit, r.Source, r.Aggregation,
			dims, r.ServingTable, r.FreshnessTargetMS, r.Accuracy, r.Description, ts,
		)
	}
	return `INSERT INTO ` + AnalyticsCatalogTable + `
		(metric, domain, type, unit, source, aggregation,
		 dimensions, serving_table, freshness_target_ms, accuracy, description, updated_at)
		VALUES ` + strings.Join(vals, ","), args
}

// SeedAnalyticsCatalog ensures the canonical analytics schema exists and upserts all
// canonical metric definitions into `analytics.catalog`.
func SeedAnalyticsCatalog(ctx context.Context) error {
	if err := EnsureAnalyticsSchema(ctx); err != nil {
		return err
	}
	rows := CanonicalCatalog()
	now := time.Now().UTC()
	for start := 0; start < len(rows); start += analyticsInsertChunk {
		end := min(start+analyticsInsertChunk, len(rows))
		stmt, args := buildCatalogInsert(rows[start:end], now)
		if err := Exec(ctx, stmt, args...); err != nil {
			return err
		}
	}
	return nil
}
