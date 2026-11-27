// Copyright © 2026 Hanzo AI. MIT License.

package datastore

import (
	"context"
	"fmt"
	"sync/atomic"
)

// analytics_schema.go — CANONICAL `analytics` CLICKHOUSE NAMESPACE.
//
// Aggregates live telemetry on ingestion via ClickHouse-native AggregatingMergeTree /
// SummingMergeTree Materialized Views with ZERO duplication of the 1.77B historical
// `event.*` rows. Financial metrics separate immutable append-only flow facts
// (`analytics.fact_commerce_ledger`) from latest-state snapshots (`analytics.state_*`).
//
// CRITICAL INVARIANT:
//   NEVER feed a mutable ReplacingMergeTree into a SummingMergeTree MV — ClickHouse MVs
//   do not reverse prior row versions on merge. `rollup_commerce_daily_mv` reads ONLY
//   from immutable append-only `analytics.fact_commerce_ledger` (MergeTree) using signed
//   integer minor-unit amounts (`sign Int8` * `*_minor Int64`).

const (
	analyticsDatabase = "analytics"
	createAnalyticsDB = `CREATE DATABASE IF NOT EXISTS analytics`

	// Governance & Metadata
	AnalyticsCatalogTable    = "analytics.catalog"
	AnalyticsWatermarksTable = "analytics.watermarks"

	// Shared Dimensions
	AnalyticsDimOrgsTable     = "analytics.dim_orgs"
	AnalyticsDimUsersTable    = "analytics.dim_users"
	AnalyticsDimModelsTable   = "analytics.dim_models"
	AnalyticsDimProductsTable = "analytics.dim_products"

	// Immutable Flow Facts & Latest-State Tables
	AnalyticsFactCommerceLedgerTable = "analytics.fact_commerce_ledger"
	AnalyticsStateOrgBalancesTable   = "analytics.state_org_balances"
	AnalyticsStateSubscriptionsTable = "analytics.state_subscriptions"
	AnalyticsStateInvoicesTable      = "analytics.state_invoices"

	// Hierarchical Incremental Rollups
	AnalyticsRollupAIMinuteTable                = "analytics.rollup_ai_minute"
	AnalyticsRollupAIHourlyTable                = "analytics.rollup_ai_hourly"
	AnalyticsRollupAIDailyTable                 = "analytics.rollup_ai_daily"
	AnalyticsRollupO11yMinuteTable              = "analytics.rollup_o11y_minute"
	AnalyticsRollupO11yHourlyTable              = "analytics.rollup_o11y_hourly"
	AnalyticsRollupAITTFTHourlyTable            = "analytics.rollup_ai_ttft_hourly"
	AnalyticsRollupSessionActivationHourlyTable = "analytics.rollup_session_activation_hourly"
	AnalyticsRollupProductHourlyTable           = "analytics.rollup_product_hourly"
	AnalyticsRollupCommerceDailyTable           = "analytics.rollup_commerce_daily"

	// L3 Materialized Serving Snapshots (O(1) point reads)
	AnalyticsServingGlobalOverviewTable    = "analytics.serving_global_overview"
	AnalyticsServingOrgOverviewTable       = "analytics.serving_org_overview"
	AnalyticsServingModelPerformanceTable  = "analytics.serving_model_performance"
	AnalyticsServingProviderEconomicsTable = "analytics.serving_provider_economics"
)

// AnalyticsDDL names one DDL statement and its optional prerequisite source table.
type AnalyticsDDL struct {
	Name       string
	SourceDB   string // optional: only execute when SourceDB.SourceTable exists
	SourceName string
	SQL        string
}

var analyticsTableDDLs = []AnalyticsDDL{
	{
		Name: AnalyticsCatalogTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.catalog (
		metric String,
		domain LowCardinality(String),
		type LowCardinality(String),
		unit LowCardinality(String),
		source LowCardinality(String),
		aggregation LowCardinality(String),
		dimensions Array(String),
		serving_table LowCardinality(String),
		freshness_target_ms UInt32,
		accuracy LowCardinality(String),
		description String,
		updated_at DateTime64(3)
	) ENGINE = ReplacingMergeTree(updated_at)
	ORDER BY (metric)`,
	},
	{
		Name: AnalyticsWatermarksTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.watermarks (
		pipeline String,
		source LowCardinality(String),
		watermark_ts DateTime64(3),
		lag_ms Int64,
		rows_processed UInt64,
		reconcile_drift_minor Int64,
		status LowCardinality(String),
		detail String,
		updated_at DateTime64(3)
	) ENGINE = ReplacingMergeTree(updated_at)
	ORDER BY (pipeline)`,
	},
	{
		Name: AnalyticsDimOrgsTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.dim_orgs (
		org String,
		display_name String,
		created_at DateTime64(3),
		is_internal UInt8,
		members UInt64,
		tier LowCardinality(String),
		status LowCardinality(String),
		updated_at DateTime64(3)
	) ENGINE = ReplacingMergeTree(updated_at)
	ORDER BY (org)`,
	},
	{
		Name: AnalyticsDimUsersTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.dim_users (
		org String,
		user_id String,
		name String,
		email String,
		created_at DateTime64(3),
		last_signin_at DateTime64(3),
		is_admin UInt8,
		forbidden UInt8,
		updated_at DateTime64(3)
	) ENGINE = ReplacingMergeTree(updated_at)
	ORDER BY (org, user_id)`,
	},
	{
		Name: AnalyticsDimModelsTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.dim_models (
		model String,
		provider LowCardinality(String),
		family LowCardinality(String),
		input_nano Int64,
		output_nano Int64,
		updated_at DateTime64(3)
	) ENGINE = ReplacingMergeTree(updated_at)
	ORDER BY (model)`,
	},
	{
		Name: AnalyticsDimProductsTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.dim_products (
		product String,
		category LowCardinality(String),
		owner_team LowCardinality(String),
		updated_at DateTime64(3)
	) ENGINE = ReplacingMergeTree(updated_at)
	ORDER BY (product)`,
	},
	{
		Name: AnalyticsFactCommerceLedgerTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.fact_commerce_ledger (
		entry_id String,
		org LowCardinality(String),
		product LowCardinality(String),
		kind LowCardinality(String),
		sign Int8,
		amount_minor Int64,
		cash_minor Int64,
		credit_minor Int64,
		currency LowCardinality(String),
		fx_rate_nano Int64,
		fx_at DateTime64(3),
		source_version UInt64,
		event_ts DateTime64(3),
		ingested_at DateTime64(3)
	) ENGINE = MergeTree()
	PARTITION BY toYYYYMM(event_ts)
	ORDER BY (event_ts, org, currency, entry_id)`,
	},
	{
		Name: AnalyticsStateOrgBalancesTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.state_org_balances (
		org String,
		currency LowCardinality(String),
		books UInt8,
		cash_minor Int64,
		credit_minor Int64,
		limit_minor Int64,
		owed_minor Int64,
		version UInt64,
		updated_at DateTime64(3)
	) ENGINE = ReplacingMergeTree(version)
	ORDER BY (org, currency)`,
	},
	{
		Name: AnalyticsStateSubscriptionsTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.state_subscriptions (
		org String,
		id String,
		deleted UInt8,
		user_id String,
		plan_id String,
		plan_name String,
		status LowCardinality(String),
		mrr_minor Int64,
		seats Int64,
		interval LowCardinality(String),
		provider LowCardinality(String),
		settled LowCardinality(String),
		currency LowCardinality(String),
		test UInt8,
		cancel_at_period_end UInt8,
		payload_json String,
		version UInt64,
		updated_at DateTime64(3)
	) ENGINE = ReplacingMergeTree(version)
	ORDER BY (org, id)`,
	},
	{
		Name: AnalyticsStateInvoicesTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.state_invoices (
		org String,
		id String,
		deleted UInt8,
		number String,
		user_id String,
		subscription_id String,
		status LowCardinality(String),
		amount_minor Int64,
		paid_minor Int64,
		currency LowCardinality(String),
		issued_at String,
		paid_at String,
		payload_json String,
		version UInt64,
		updated_at DateTime64(3)
	) ENGINE = ReplacingMergeTree(version)
	ORDER BY (org, id)`,
	},
	{
		Name: AnalyticsRollupAIMinuteTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.rollup_ai_minute (
		minute DateTime,
		org LowCardinality(String),
		model LowCardinality(String),
		provider LowCardinality(String),
		requests AggregateFunction(count),
		tokens AggregateFunction(sum, Int64),
		prompt_tokens AggregateFunction(sum, Int64),
		completion_tokens AggregateFunction(sum, Int64),
		cache_read_tokens AggregateFunction(sum, Int64),
		cost_nano AggregateFunction(sum, Int64),
		billed_nano AggregateFunction(sum, Int64),
		margin_nano AggregateFunction(sum, Int64),
		cache_saved_nano AggregateFunction(sum, Int64),
		errors AggregateFunction(sum, UInt64),
		users AggregateFunction(uniq, String)
	) ENGINE = AggregatingMergeTree()
	PARTITION BY toYYYYMMDD(minute)
	ORDER BY (minute, org, model, provider)
	TTL minute + INTERVAL 48 HOUR`,
	},
	{
		Name: AnalyticsRollupAIHourlyTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.rollup_ai_hourly (
		hour DateTime,
		org LowCardinality(String),
		model LowCardinality(String),
		provider LowCardinality(String),
		requests AggregateFunction(count),
		tokens AggregateFunction(sum, Int64),
		prompt_tokens AggregateFunction(sum, Int64),
		completion_tokens AggregateFunction(sum, Int64),
		cache_read_tokens AggregateFunction(sum, Int64),
		cost_nano AggregateFunction(sum, Int64),
		billed_nano AggregateFunction(sum, Int64),
		margin_nano AggregateFunction(sum, Int64),
		cache_saved_nano AggregateFunction(sum, Int64),
		errors AggregateFunction(sum, UInt64),
		users AggregateFunction(uniq, String)
	) ENGINE = AggregatingMergeTree()
	PARTITION BY toYYYYMM(hour)
	ORDER BY (hour, org, model, provider)`,
	},
	{
		Name: AnalyticsRollupAIDailyTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.rollup_ai_daily (
		day Date,
		org LowCardinality(String),
		model LowCardinality(String),
		provider LowCardinality(String),
		requests AggregateFunction(count),
		tokens AggregateFunction(sum, Int64),
		prompt_tokens AggregateFunction(sum, Int64),
		completion_tokens AggregateFunction(sum, Int64),
		cache_read_tokens AggregateFunction(sum, Int64),
		cost_nano AggregateFunction(sum, Int64),
		billed_nano AggregateFunction(sum, Int64),
		margin_nano AggregateFunction(sum, Int64),
		cache_saved_nano AggregateFunction(sum, Int64),
		errors AggregateFunction(sum, UInt64),
		users AggregateFunction(uniq, String)
	) ENGINE = AggregatingMergeTree()
	PARTITION BY toYYYYMM(day)
	ORDER BY (day, org, model, provider)`,
	},
	{
		Name: AnalyticsRollupO11yMinuteTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.rollup_o11y_minute (
		minute DateTime,
		org LowCardinality(String),
		service LowCardinality(String),
		spans AggregateFunction(count),
		errors AggregateFunction(sum, UInt64),
		p50_ns AggregateFunction(quantileTDigest(0.50), UInt64),
		p95_ns AggregateFunction(quantileTDigest(0.95), UInt64),
		p99_ns AggregateFunction(quantileTDigest(0.99), UInt64)
	) ENGINE = AggregatingMergeTree()
	PARTITION BY toYYYYMMDD(minute)
	ORDER BY (minute, org, service)
	TTL minute + INTERVAL 48 HOUR`,
	},
	{
		Name: AnalyticsRollupO11yHourlyTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.rollup_o11y_hourly (
		hour DateTime,
		org LowCardinality(String),
		service LowCardinality(String),
		spans AggregateFunction(count),
		errors AggregateFunction(sum, UInt64),
		p50_ns AggregateFunction(quantileTDigest(0.50), UInt64),
		p95_ns AggregateFunction(quantileTDigest(0.95), UInt64),
		p99_ns AggregateFunction(quantileTDigest(0.99), UInt64)
	) ENGINE = AggregatingMergeTree()
	PARTITION BY toYYYYMM(hour)
	ORDER BY (hour, org, service)`,
	},
	{
		Name: AnalyticsRollupAITTFTHourlyTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.rollup_ai_ttft_hourly (
		hour DateTime,
		org LowCardinality(String),
		model LowCardinality(String),
		ttft_count AggregateFunction(sum, UInt64),
		ttft_p50 AggregateFunction(quantileTDigestIf(0.50), Float64, UInt8),
		ttft_p95 AggregateFunction(quantileTDigestIf(0.95), Float64, UInt8),
		ttft_p99 AggregateFunction(quantileTDigestIf(0.99), Float64, UInt8),
		gen_count AggregateFunction(count),
		gen_cost_usd AggregateFunction(sum, Float64),
		gen_input_tokens AggregateFunction(sum, UInt64),
		gen_output_tokens AggregateFunction(sum, UInt64),
		gen_errors AggregateFunction(sum, UInt64),
		gen_duration_sum_ns AggregateFunction(sum, UInt64),
		gen_duration_count AggregateFunction(sum, UInt64),
		gen_p95_ns AggregateFunction(quantileTDigestIf(0.95), UInt64, UInt8)
	) ENGINE = AggregatingMergeTree()
	PARTITION BY toYYYYMM(hour)
	ORDER BY (hour, org, model)`,
	},
	{
		Name: AnalyticsRollupSessionActivationHourlyTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.rollup_session_activation_hourly (
		hour DateTime,
		org LowCardinality(String),
		kind LowCardinality(String),
		activations AggregateFunction(count),
		errors AggregateFunction(sum, UInt64),
		p50_ns AggregateFunction(quantileTDigestIf(0.50), UInt64, UInt8),
		p99_ns AggregateFunction(quantileTDigestIf(0.99), UInt64, UInt8)
	) ENGINE = AggregatingMergeTree()
	PARTITION BY toYYYYMM(hour)
	ORDER BY (hour, org, kind)`,
	},
	{
		Name: AnalyticsRollupProductHourlyTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.rollup_product_hourly (
		hour DateTime,
		org LowCardinality(String),
		product LowCardinality(String),
		name String,
		events AggregateFunction(count),
		users AggregateFunction(uniq, String),
		sessions AggregateFunction(uniq, String),
		bytes AggregateFunction(sum, UInt64)
	) ENGINE = AggregatingMergeTree()
	PARTITION BY toYYYYMM(hour)
	ORDER BY (hour, org, product, name)`,
	},
	{
		Name: AnalyticsRollupCommerceDailyTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.rollup_commerce_daily (
		day Date,
		org LowCardinality(String),
		product LowCardinality(String),
		currency LowCardinality(String),
		kind LowCardinality(String),
		amount_minor Int64,
		cash_minor Int64,
		credit_minor Int64,
		entries Int64
	) ENGINE = SummingMergeTree()
	PARTITION BY toYYYYMM(day)
	ORDER BY (day, org, product, currency, kind)`,
	},
	{
		Name: AnalyticsServingGlobalOverviewTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.serving_global_overview (
		scope String DEFAULT 'global',
		orgs_total UInt64,
		customer_orgs UInt64,
		members_total UInt64,
		orgs_with_books UInt64,
		cash_minor Int64,
		credit_minor Int64,
		owed_minor Int64,
		active_subscriptions UInt64,
		mrr_minor Int64,
		ai_requests_30d UInt64,
		ai_tokens_30d Int64,
		ai_cost_nano_30d Int64,
		ai_billed_nano_30d Int64,
		ai_margin_nano_30d Int64,
		ai_cache_saved_nano_30d Int64,
		spans_24h UInt64,
		errors_24h UInt64,
		refreshed_at DateTime64(3)
	) ENGINE = ReplacingMergeTree(refreshed_at)
	ORDER BY (scope, refreshed_at)
	SETTINGS index_granularity = 64`,
	},
	{
		Name: AnalyticsServingOrgOverviewTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.serving_org_overview (
		org String,
		display_name String,
		is_internal UInt8,
		members UInt64,
		tier LowCardinality(String),
		status LowCardinality(String),
		created_at DateTime64(3),
		cash_minor Int64,
		credit_minor Int64,
		owed_minor Int64,
		mrr_minor Int64,
		seats Int64,
		ai_requests_30d UInt64,
		ai_tokens_30d Int64,
		ai_cost_nano_30d Int64,
		ai_billed_nano_30d Int64,
		refreshed_at DateTime64(3)
	) ENGINE = ReplacingMergeTree(refreshed_at)
	ORDER BY (org)
	SETTINGS index_granularity = 256`,
	},
	{
		Name: AnalyticsServingModelPerformanceTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.serving_model_performance (
		model String,
		provider LowCardinality(String),
		family LowCardinality(String),
		requests UInt64,
		tokens Int64,
		prompt_tokens Int64,
		completion_tokens Int64,
		cache_read_tokens Int64,
		cost_nano Int64,
		billed_nano Int64,
		margin_nano Int64,
		cache_saved_nano Int64,
		errors UInt64,
		error_rate Float64,
		active_users UInt64,
		active_orgs UInt64,
		refreshed_at DateTime64(3)
	) ENGINE = ReplacingMergeTree(refreshed_at)
	ORDER BY (model, provider)`,
	},
	{
		Name: AnalyticsServingProviderEconomicsTable,
		SQL: `CREATE TABLE IF NOT EXISTS analytics.serving_provider_economics (
		provider LowCardinality(String),
		requests UInt64,
		tokens Int64,
		cost_nano Int64,
		billed_nano Int64,
		margin_nano Int64,
		margin_bps Int64,
		cache_saved_nano Int64,
		errors UInt64,
		error_rate Float64,
		models UInt64,
		active_orgs UInt64,
		refreshed_at DateTime64(3)
	) ENGINE = ReplacingMergeTree(refreshed_at)
	ORDER BY (provider)`,
	},
}

var analyticsMVDDLs = []AnalyticsDDL{
	{
		Name:       "analytics.mv_ai_minute",
		SourceDB:   "hanzo",
		SourceName: "cloud_usage",
		SQL: `CREATE MATERIALIZED VIEW IF NOT EXISTS analytics.mv_ai_minute
	TO analytics.rollup_ai_minute AS
	SELECT
		toStartOfMinute(timestamp) AS minute,
		toLowCardinality(organization) AS org,
		toLowCardinality(model) AS model,
		toLowCardinality(provider) AS provider,
		countState() AS requests,
		sumState(toInt64(total_tokens)) AS tokens,
		sumState(toInt64(prompt_tokens)) AS prompt_tokens,
		sumState(toInt64(completion_tokens)) AS completion_tokens,
		sumState(toInt64(cache_read_tokens)) AS cache_read_tokens,
		sumState(cost_nano) AS cost_nano,
		sumState(billed_nano) AS billed_nano,
		sumState(margin_nano) AS margin_nano,
		sumState(cache_saved_nano) AS cache_saved_nano,
		sumState(toUInt64(status = 'error')) AS errors,
		uniqState(user_id) AS users
	FROM hanzo.cloud_usage
	GROUP BY minute, org, model, provider`,
	},
	{
		Name:       "analytics.rollup_ai_hourly_mv",
		SourceDB:   "hanzo",
		SourceName: "cloud_usage",
		SQL: `CREATE MATERIALIZED VIEW IF NOT EXISTS analytics.rollup_ai_hourly_mv
	TO analytics.rollup_ai_hourly AS
	SELECT
		toStartOfHour(timestamp) AS hour,
		toLowCardinality(organization) AS org,
		toLowCardinality(model) AS model,
		toLowCardinality(provider) AS provider,
		countState() AS requests,
		sumState(toInt64(total_tokens)) AS tokens,
		sumState(toInt64(prompt_tokens)) AS prompt_tokens,
		sumState(toInt64(completion_tokens)) AS completion_tokens,
		sumState(toInt64(cache_read_tokens)) AS cache_read_tokens,
		sumState(cost_nano) AS cost_nano,
		sumState(billed_nano) AS billed_nano,
		sumState(margin_nano) AS margin_nano,
		sumState(cache_saved_nano) AS cache_saved_nano,
		sumState(toUInt64(status = 'error')) AS errors,
		uniqState(user_id) AS users
	FROM hanzo.cloud_usage
	GROUP BY hour, org, model, provider`,
	},
	{
		Name:       "analytics.rollup_ai_daily_mv",
		SourceDB:   "hanzo",
		SourceName: "cloud_usage",
		SQL: `CREATE MATERIALIZED VIEW IF NOT EXISTS analytics.rollup_ai_daily_mv
	TO analytics.rollup_ai_daily AS
	SELECT
		toDate(timestamp) AS day,
		toLowCardinality(organization) AS org,
		toLowCardinality(model) AS model,
		toLowCardinality(provider) AS provider,
		countState() AS requests,
		sumState(toInt64(total_tokens)) AS tokens,
		sumState(toInt64(prompt_tokens)) AS prompt_tokens,
		sumState(toInt64(completion_tokens)) AS completion_tokens,
		sumState(toInt64(cache_read_tokens)) AS cache_read_tokens,
		sumState(cost_nano) AS cost_nano,
		sumState(billed_nano) AS billed_nano,
		sumState(margin_nano) AS margin_nano,
		sumState(cache_saved_nano) AS cache_saved_nano,
		sumState(toUInt64(status = 'error')) AS errors,
		uniqState(user_id) AS users
	FROM hanzo.cloud_usage
	GROUP BY day, org, model, provider`,
	},
	{
		Name:       "analytics.mv_o11y_minute",
		SourceDB:   "event",
		SourceName: "span",
		SQL: `CREATE MATERIALIZED VIEW IF NOT EXISTS analytics.mv_o11y_minute
	TO analytics.rollup_o11y_minute AS
	SELECT
		toDateTime(toStartOfMinute(time)) AS minute,
		org,
		service,
		countState() AS spans,
		sumState(toUInt64(status = 'error')) AS errors,
		quantileTDigestState(0.50)(duration) AS p50_ns,
		quantileTDigestState(0.95)(duration) AS p95_ns,
		quantileTDigestState(0.99)(duration) AS p99_ns
	FROM event.span
	GROUP BY minute, org, service`,
	},
	{
		Name:       "analytics.rollup_o11y_hourly_mv",
		SourceDB:   "event",
		SourceName: "span",
		SQL: `CREATE MATERIALIZED VIEW IF NOT EXISTS analytics.rollup_o11y_hourly_mv
	TO analytics.rollup_o11y_hourly AS
	SELECT
		toDateTime(toStartOfHour(time)) AS hour,
		org,
		service,
		countState() AS spans,
		sumState(toUInt64(status = 'error')) AS errors,
		quantileTDigestState(0.50)(duration) AS p50_ns,
		quantileTDigestState(0.95)(duration) AS p95_ns,
		quantileTDigestState(0.99)(duration) AS p99_ns
	FROM event.span
	GROUP BY hour, org, service`,
	},
	{
		Name:       "analytics.mv_ai_ttft_hourly",
		SourceDB:   "event",
		SourceName: "span",
		SQL: `CREATE MATERIALIZED VIEW IF NOT EXISTS analytics.mv_ai_ttft_hourly
	TO analytics.rollup_ai_ttft_hourly AS
	SELECT
		toDateTime(toStartOfHour(time)) AS hour,
		org,
		toLowCardinality(if(attributes['gen_ai.response.model'] != '', attributes['gen_ai.response.model'], attributes['gen_ai.request.model'])) AS model,
		sumState(toUInt64(mapContains(attributes, 'hanzo.ai.ttft_ms'))) AS ttft_count,
		quantileTDigestIfState(0.50)(toFloat64OrZero(attributes['hanzo.ai.ttft_ms']), toUInt8(mapContains(attributes, 'hanzo.ai.ttft_ms'))) AS ttft_p50,
		quantileTDigestIfState(0.95)(toFloat64OrZero(attributes['hanzo.ai.ttft_ms']), toUInt8(mapContains(attributes, 'hanzo.ai.ttft_ms'))) AS ttft_p95,
		quantileTDigestIfState(0.99)(toFloat64OrZero(attributes['hanzo.ai.ttft_ms']), toUInt8(mapContains(attributes, 'hanzo.ai.ttft_ms'))) AS ttft_p99,
		countState() AS gen_count,
		sumState(toFloat64OrZero(attributes['_o11y.gen_ai.total_cost'])) AS gen_cost_usd,
		sumState(toUInt64OrZero(if(attributes['gen_ai.usage.input_tokens'] != '', attributes['gen_ai.usage.input_tokens'], attributes['_o11y.gen_ai.usage.input_tokens']))) AS gen_input_tokens,
		sumState(toUInt64OrZero(if(attributes['gen_ai.usage.output_tokens'] != '', attributes['gen_ai.usage.output_tokens'], attributes['_o11y.gen_ai.usage.output_tokens']))) AS gen_output_tokens,
		sumState(toUInt64(status = 'error')) AS gen_errors,
		sumState(if(duration > 0, toUInt64(duration), toUInt64(0))) AS gen_duration_sum_ns,
		sumState(toUInt64(duration > 0)) AS gen_duration_count,
		quantileTDigestIfState(0.95)(toUInt64(duration), toUInt8(duration > 0)) AS gen_p95_ns
	FROM event.span
	WHERE kind = 'client' AND mapContains(attributes, 'gen_ai.system')
	GROUP BY
		toDateTime(toStartOfHour(time)),
		org,
		toLowCardinality(if(attributes['gen_ai.response.model'] != '', attributes['gen_ai.response.model'], attributes['gen_ai.request.model']))`,
	},
	{
		Name:       "analytics.mv_session_activation_hourly",
		SourceDB:   "event",
		SourceName: "span",
		SQL: `CREATE MATERIALIZED VIEW IF NOT EXISTS analytics.mv_session_activation_hourly
	TO analytics.rollup_session_activation_hourly AS
	SELECT
		toDateTime(toStartOfHour(time)) AS hour,
		org,
		toLowCardinality(attributes['hanzo.session.activation']) AS kind,
		countState() AS activations,
		sumState(toUInt64(status = 'error')) AS errors,
		quantileTDigestIfState(0.50)(toUInt64(duration), toUInt8(status != 'error')) AS p50_ns,
		quantileTDigestIfState(0.99)(toUInt64(duration), toUInt8(status != 'error')) AS p99_ns
	FROM event.span
	WHERE name = 'hanzo.session.activate' AND attributes['hanzo.session.activation'] != ''
	GROUP BY
		toDateTime(toStartOfHour(time)),
		org,
		toLowCardinality(attributes['hanzo.session.activation'])`,
	},
	{
		Name:       "analytics.rollup_product_hourly_mv",
		SourceDB:   "event",
		SourceName: "fact",
		SQL: `CREATE MATERIALIZED VIEW IF NOT EXISTS analytics.rollup_product_hourly_mv
	TO analytics.rollup_product_hourly AS
	SELECT
		toDateTime(toStartOfHour(time)) AS hour,
		org,
		product,
		name,
		countState() AS events,
		uniqState(person_id) AS users,
		uniqState(session_id) AS sessions,
		sumState(bytes) AS bytes
	FROM event.fact
	GROUP BY hour, org, product, name`,
	},
	{
		Name:       "analytics.rollup_commerce_daily_mv",
		SourceDB:   "analytics",
		SourceName: "fact_commerce_ledger",
		SQL: `CREATE MATERIALIZED VIEW IF NOT EXISTS analytics.rollup_commerce_daily_mv
	TO analytics.rollup_commerce_daily AS
	SELECT
		toDate(event_ts) AS day,
		org,
		product,
		currency,
		kind,
		sum(toInt64(sign) * amount_minor) AS amount_minor,
		sum(toInt64(sign) * cash_minor) AS cash_minor,
		sum(toInt64(sign) * credit_minor) AS credit_minor,
		sum(toInt64(sign)) AS entries
	FROM analytics.fact_commerce_ledger
	GROUP BY day, org, product, currency, kind`,
	},
}

// Constant-cost serving views built exclusively over pre-aggregated rollups,
// latest-state snapshots, and compact dimension tables (never scanning raw event.*).
var analyticsViewDDLs = []AnalyticsDDL{
	{
		Name: "analytics.global_overview",
		SQL: `CREATE OR REPLACE VIEW analytics.global_overview AS
	SELECT
		o.orgs_total,
		o.customer_orgs,
		o.members_total,
		b.orgs_with_books,
		b.cash_minor,
		b.credit_minor,
		b.owed_minor,
		s.active_subscriptions,
		s.mrr_minor,
		a.ai_requests_30d,
		a.ai_tokens_30d,
		a.ai_cost_nano_30d,
		a.ai_billed_nano_30d,
		a.ai_margin_nano_30d,
		a.ai_cache_saved_nano_30d,
		h.spans_24h,
		h.errors_24h
	FROM (
		SELECT
			count() AS orgs_total,
			countIf(is_internal = 0) AS customer_orgs,
			sum(members) AS members_total
		FROM (
			SELECT
				org,
				argMax(is_internal, updated_at) AS is_internal,
				argMax(members, updated_at) AS members
			FROM analytics.dim_orgs
			GROUP BY org
		)
	) AS o
	CROSS JOIN (
		SELECT
			countIf(books = 1) AS orgs_with_books,
			sum(cash_minor) AS cash_minor,
			sum(credit_minor) AS credit_minor,
			sum(owed_minor) AS owed_minor
		FROM (
			SELECT
				org,
				currency,
				argMax(books, version) AS books,
				argMax(cash_minor, version) AS cash_minor,
				argMax(credit_minor, version) AS credit_minor,
				argMax(owed_minor, version) AS owed_minor
			FROM analytics.state_org_balances
			GROUP BY org, currency
		)
	) AS b
	CROSS JOIN (
		SELECT
			countIf(deleted = 0 AND test = 0 AND status IN ('active', 'trialing')) AS active_subscriptions,
			sumIf(mrr_minor, deleted = 0 AND test = 0 AND status = 'active') AS mrr_minor
		FROM (
			SELECT
				org,
				id,
				argMax(deleted, version) AS deleted,
				argMax(test, version) AS test,
				argMax(status, version) AS status,
				argMax(mrr_minor, version) AS mrr_minor
			FROM analytics.state_subscriptions
			GROUP BY org, id
		)
	) AS s
	CROSS JOIN (
		SELECT
			countMerge(requests) AS ai_requests_30d,
			sumMerge(tokens) AS ai_tokens_30d,
			sumMerge(cost_nano) AS ai_cost_nano_30d,
			sumMerge(billed_nano) AS ai_billed_nano_30d,
			sumMerge(margin_nano) AS ai_margin_nano_30d,
			sumMerge(cache_saved_nano) AS ai_cache_saved_nano_30d
		FROM analytics.rollup_ai_daily
		WHERE day >= today() - 30
	) AS a
	CROSS JOIN (
		SELECT
			countMerge(spans) AS spans_24h,
			sumMerge(errors) AS errors_24h
		FROM analytics.rollup_o11y_hourly
		WHERE hour >= now() - INTERVAL 24 HOUR
	) AS h`,
	},
	{
		Name: "analytics.global_finance",
		SQL: `CREATE OR REPLACE VIEW analytics.global_finance AS
	SELECT
		b.currency AS currency,
		b.orgs_with_books,
		b.cash_minor,
		b.credit_minor,
		b.limit_minor,
		b.owed_minor,
		coalesce(s.active_subscriptions, 0) AS active_subscriptions,
		coalesce(s.mrr_minor, 0) AS mrr_minor,
		coalesce(c.deposits_30d_minor, 0) AS deposits_30d_minor,
		coalesce(c.usage_30d_minor, 0) AS usage_30d_minor,
		coalesce(c.refunds_30d_minor, 0) AS refunds_30d_minor,
		coalesce(c.grants_30d_minor, 0) AS grants_30d_minor,
		coalesce(c.invoices_paid_30d_minor, 0) AS invoices_paid_30d_minor
	FROM (
		SELECT
			currency,
			countIf(books = 1) AS orgs_with_books,
			sum(cash_minor) AS cash_minor,
			sum(credit_minor) AS credit_minor,
			sum(limit_minor) AS limit_minor,
			sum(owed_minor) AS owed_minor
		FROM (
			SELECT
				org,
				currency,
				argMax(books, version) AS books,
				argMax(cash_minor, version) AS cash_minor,
				argMax(credit_minor, version) AS credit_minor,
				argMax(limit_minor, version) AS limit_minor,
				argMax(owed_minor, version) AS owed_minor
			FROM analytics.state_org_balances
			GROUP BY org, currency
		)
		GROUP BY currency
	) AS b
	LEFT JOIN (
		SELECT
			currency,
			countIf(deleted = 0 AND test = 0 AND status IN ('active', 'trialing')) AS active_subscriptions,
			sumIf(mrr_minor, deleted = 0 AND test = 0 AND status = 'active') AS mrr_minor
		FROM (
			SELECT
				org,
				id,
				argMax(currency, version) AS currency,
				argMax(deleted, version) AS deleted,
				argMax(test, version) AS test,
				argMax(status, version) AS status,
				argMax(mrr_minor, version) AS mrr_minor
			FROM analytics.state_subscriptions
			GROUP BY org, id
		)
		GROUP BY currency
	) AS s USING (currency)
	LEFT JOIN (
		SELECT
			currency,
			sumIf(amount_minor, kind = 'deposit') AS deposits_30d_minor,
			sumIf(amount_minor, kind = 'usage') AS usage_30d_minor,
			sumIf(amount_minor, kind = 'refund') AS refunds_30d_minor,
			sumIf(amount_minor, kind = 'grant') AS grants_30d_minor,
			sumIf(amount_minor, kind = 'invoice_paid') AS invoices_paid_30d_minor
		FROM analytics.rollup_commerce_daily
		WHERE day >= today() - 30
		GROUP BY currency
	) AS c USING (currency)`,
	},
	{
		Name: "analytics.global_usage",
		SQL: `CREATE OR REPLACE VIEW analytics.global_usage AS
	SELECT
		day,
		countMerge(requests) AS requests,
		sumMerge(tokens) AS tokens,
		sumMerge(prompt_tokens) AS prompt_tokens,
		sumMerge(completion_tokens) AS completion_tokens,
		sumMerge(cache_read_tokens) AS cache_read_tokens,
		sumMerge(cost_nano) AS cost_nano,
		sumMerge(billed_nano) AS billed_nano,
		sumMerge(margin_nano) AS margin_nano,
		sumMerge(cache_saved_nano) AS cache_saved_nano,
		sumMerge(errors) AS errors,
		uniqMerge(users) AS active_users,
		uniqExact(org) AS active_orgs
	FROM analytics.rollup_ai_daily
	GROUP BY day
	ORDER BY day DESC`,
	},
	{
		Name: "analytics.global_compute",
		SQL: `CREATE OR REPLACE VIEW analytics.global_compute AS
	SELECT
		hour,
		service,
		countMerge(spans) AS spans,
		sumMerge(errors) AS errors,
		if(spans > 0, toFloat64(errors) / toFloat64(spans), 0.0) AS error_rate,
		quantileTDigestMerge(0.50)(p50_ns) AS p50_ns,
		quantileTDigestMerge(0.95)(p95_ns) AS p95_ns,
		quantileTDigestMerge(0.99)(p99_ns) AS p99_ns,
		uniqExact(org) AS active_orgs
	FROM analytics.rollup_o11y_hourly
	GROUP BY hour, service
	ORDER BY hour DESC, spans DESC`,
	},
	{
		Name: "analytics.global_health",
		SQL: `CREATE OR REPLACE VIEW analytics.global_health AS
	SELECT
		service,
		countMerge(spans) AS spans_24h,
		sumMerge(errors) AS errors_24h,
		if(spans_24h > 0, toFloat64(errors_24h) / toFloat64(spans_24h), 0.0) AS error_rate,
		quantileTDigestMerge(0.50)(p50_ns) AS p50_ns,
		quantileTDigestMerge(0.95)(p95_ns) AS p95_ns,
		quantileTDigestMerge(0.99)(p99_ns) AS p99_ns,
		uniqExact(org) AS affected_orgs
	FROM analytics.rollup_o11y_hourly
	WHERE hour >= now() - INTERVAL 24 HOUR
	GROUP BY service
	ORDER BY errors_24h DESC, spans_24h DESC`,
	},
	{
		Name: "analytics.global_growth",
		SQL: `CREATE OR REPLACE VIEW analytics.global_growth AS
	SELECT
		coalesce(o.day, p.day) AS day,
		coalesce(o.new_orgs, 0) AS new_orgs,
		coalesce(o.new_customer_orgs, 0) AS new_customer_orgs,
		coalesce(p.events, 0) AS events,
		coalesce(p.active_users, 0) AS active_users,
		coalesce(p.sessions, 0) AS sessions
	FROM (
		SELECT
			toDate(created_at) AS day,
			count() AS new_orgs,
			countIf(is_internal = 0) AS new_customer_orgs
		FROM (
			SELECT
				org,
				argMax(created_at, updated_at) AS created_at,
				argMax(is_internal, updated_at) AS is_internal
			FROM analytics.dim_orgs
			GROUP BY org
		)
		GROUP BY day
	) AS o
	FULL OUTER JOIN (
		SELECT
			toDate(hour) AS day,
			countMerge(events) AS events,
			uniqMerge(users) AS active_users,
			uniqMerge(sessions) AS sessions
		FROM analytics.rollup_product_hourly
		GROUP BY day
	) AS p USING (day)
	ORDER BY day DESC`,
	},
	{
		Name: "analytics.global_security",
		SQL: `CREATE OR REPLACE VIEW analytics.global_security AS
	SELECT
		u.org AS org,
		u.total_users,
		u.admin_users,
		u.forbidden_users,
		u.active_7d_users,
		coalesce(e.errors_24h, 0) AS errors_24h,
		coalesce(e.spans_24h, 0) AS spans_24h
	FROM (
		SELECT
			org,
			count() AS total_users,
			countIf(is_admin = 1) AS admin_users,
			countIf(forbidden = 1) AS forbidden_users,
			countIf(last_signin_at >= now() - INTERVAL 7 DAY) AS active_7d_users
		FROM (
			SELECT
				org,
				user_id,
				argMax(is_admin, updated_at) AS is_admin,
				argMax(forbidden, updated_at) AS forbidden,
				argMax(last_signin_at, updated_at) AS last_signin_at
			FROM analytics.dim_users
			GROUP BY org, user_id
		)
		GROUP BY org
	) AS u
	LEFT JOIN (
		SELECT
			org,
			countMerge(spans) AS spans_24h,
			sumMerge(errors) AS errors_24h
		FROM analytics.rollup_o11y_hourly
		WHERE hour >= now() - INTERVAL 24 HOUR
		GROUP BY org
	) AS e USING (org)`,
	},
	{
		Name: "analytics.org_overview",
		SQL: `CREATE OR REPLACE VIEW analytics.org_overview AS
	SELECT
		o.org AS org,
		o.display_name,
		o.is_internal,
		o.members,
		o.tier,
		o.status,
		o.created_at,
		coalesce(b.cash_minor, 0) AS cash_minor,
		coalesce(b.credit_minor, 0) AS credit_minor,
		coalesce(b.owed_minor, 0) AS owed_minor,
		coalesce(s.mrr_minor, 0) AS mrr_minor,
		coalesce(s.seats, 0) AS seats,
		coalesce(a.ai_requests_30d, 0) AS ai_requests_30d,
		coalesce(a.ai_tokens_30d, 0) AS ai_tokens_30d,
		coalesce(a.ai_cost_nano_30d, 0) AS ai_cost_nano_30d,
		coalesce(a.ai_billed_nano_30d, 0) AS ai_billed_nano_30d
	FROM (
		SELECT
			org,
			argMax(display_name, updated_at) AS display_name,
			argMax(is_internal, updated_at) AS is_internal,
			argMax(members, updated_at) AS members,
			argMax(tier, updated_at) AS tier,
			argMax(status, updated_at) AS status,
			argMax(created_at, updated_at) AS created_at
		FROM analytics.dim_orgs
		GROUP BY org
	) AS o
	LEFT JOIN (
		SELECT
			org,
			sum(cash_minor) AS cash_minor,
			sum(credit_minor) AS credit_minor,
			sum(owed_minor) AS owed_minor
		FROM (
			SELECT
				org,
				currency,
				argMax(cash_minor, version) AS cash_minor,
				argMax(credit_minor, version) AS credit_minor,
				argMax(owed_minor, version) AS owed_minor
			FROM analytics.state_org_balances
			GROUP BY org, currency
		)
		GROUP BY org
	) AS b USING (org)
	LEFT JOIN (
		SELECT
			org,
			sumIf(mrr_minor, deleted = 0 AND test = 0 AND status = 'active') AS mrr_minor,
			sumIf(seats, deleted = 0 AND test = 0 AND status IN ('active', 'trialing')) AS seats
		FROM (
			SELECT
				org,
				id,
				argMax(deleted, version) AS deleted,
				argMax(test, version) AS test,
				argMax(status, version) AS status,
				argMax(mrr_minor, version) AS mrr_minor,
				argMax(seats, version) AS seats
			FROM analytics.state_subscriptions
			GROUP BY org, id
		)
		GROUP BY org
	) AS s USING (org)
	LEFT JOIN (
		SELECT
			org,
			countMerge(requests) AS ai_requests_30d,
			sumMerge(tokens) AS ai_tokens_30d,
			sumMerge(cost_nano) AS ai_cost_nano_30d,
			sumMerge(billed_nano) AS ai_billed_nano_30d
		FROM analytics.rollup_ai_daily
		WHERE day >= today() - 30
		GROUP BY org
	) AS a USING (org)`,
	},
	{
		Name: "analytics.org_finance",
		SQL: `CREATE OR REPLACE VIEW analytics.org_finance AS
	SELECT
		b.org AS org,
		b.currency AS currency,
		b.books,
		b.cash_minor,
		b.credit_minor,
		b.limit_minor,
		b.owed_minor,
		coalesce(s.mrr_minor, 0) AS mrr_minor,
		coalesce(c.deposits_30d_minor, 0) AS deposits_30d_minor,
		coalesce(c.usage_30d_minor, 0) AS usage_30d_minor,
		coalesce(c.refunds_30d_minor, 0) AS refunds_30d_minor,
		coalesce(c.grants_30d_minor, 0) AS grants_30d_minor,
		coalesce(i.open_invoices_minor, 0) AS open_invoices_minor,
		coalesce(i.paid_invoices_minor, 0) AS paid_invoices_minor
	FROM (
		SELECT
			org,
			currency,
			argMax(books, version) AS books,
			argMax(cash_minor, version) AS cash_minor,
			argMax(credit_minor, version) AS credit_minor,
			argMax(limit_minor, version) AS limit_minor,
			argMax(owed_minor, version) AS owed_minor
		FROM analytics.state_org_balances
		GROUP BY org, currency
	) AS b
	LEFT JOIN (
		SELECT
			org,
			currency,
			sumIf(mrr_minor, deleted = 0 AND test = 0 AND status = 'active') AS mrr_minor
		FROM (
			SELECT
				org,
				id,
				argMax(currency, version) AS currency,
				argMax(deleted, version) AS deleted,
				argMax(test, version) AS test,
				argMax(status, version) AS status,
				argMax(mrr_minor, version) AS mrr_minor
			FROM analytics.state_subscriptions
			GROUP BY org, id
		)
		GROUP BY org, currency
	) AS s USING (org, currency)
	LEFT JOIN (
		SELECT
			org,
			currency,
			sumIf(amount_minor, kind = 'deposit') AS deposits_30d_minor,
			sumIf(amount_minor, kind = 'usage') AS usage_30d_minor,
			sumIf(amount_minor, kind = 'refund') AS refunds_30d_minor,
			sumIf(amount_minor, kind = 'grant') AS grants_30d_minor
		FROM analytics.rollup_commerce_daily
		WHERE day >= today() - 30
		GROUP BY org, currency
	) AS c USING (org, currency)
	LEFT JOIN (
		SELECT
			org,
			currency,
			sumIf(amount_minor - paid_minor, deleted = 0 AND status IN ('open', 'past_due')) AS open_invoices_minor,
			sumIf(paid_minor, deleted = 0 AND status = 'paid') AS paid_invoices_minor
		FROM (
			SELECT
				org,
				id,
				argMax(currency, version) AS currency,
				argMax(deleted, version) AS deleted,
				argMax(status, version) AS status,
				argMax(amount_minor, version) AS amount_minor,
				argMax(paid_minor, version) AS paid_minor
			FROM analytics.state_invoices
			GROUP BY org, id
		)
		GROUP BY org, currency
	) AS i USING (org, currency)`,
	},
	{
		Name: "analytics.org_usage",
		SQL: `CREATE OR REPLACE VIEW analytics.org_usage AS
	SELECT
		day,
		org,
		countMerge(requests) AS requests,
		sumMerge(tokens) AS tokens,
		sumMerge(prompt_tokens) AS prompt_tokens,
		sumMerge(completion_tokens) AS completion_tokens,
		sumMerge(cache_read_tokens) AS cache_read_tokens,
		sumMerge(cost_nano) AS cost_nano,
		sumMerge(billed_nano) AS billed_nano,
		sumMerge(margin_nano) AS margin_nano,
		sumMerge(cache_saved_nano) AS cache_saved_nano,
		sumMerge(errors) AS errors,
		uniqMerge(users) AS active_users
	FROM analytics.rollup_ai_daily
	GROUP BY day, org
	ORDER BY day DESC, billed_nano DESC`,
	},
	{
		Name: "analytics.org_compute",
		SQL: `CREATE OR REPLACE VIEW analytics.org_compute AS
	SELECT
		hour,
		org,
		service,
		countMerge(spans) AS spans,
		sumMerge(errors) AS errors,
		if(spans > 0, toFloat64(errors) / toFloat64(spans), 0.0) AS error_rate,
		quantileTDigestMerge(0.50)(p50_ns) AS p50_ns,
		quantileTDigestMerge(0.95)(p95_ns) AS p95_ns,
		quantileTDigestMerge(0.99)(p99_ns) AS p99_ns
	FROM analytics.rollup_o11y_hourly
	GROUP BY hour, org, service
	ORDER BY hour DESC, spans DESC`,
	},
	{
		Name: "analytics.model_performance",
		SQL: `CREATE OR REPLACE VIEW analytics.model_performance AS
	SELECT
		u.model,
		u.provider,
		coalesce(m.family, '') AS family,
		u.requests,
		u.tokens,
		u.prompt_tokens,
		u.completion_tokens,
		u.cache_read_tokens,
		u.cost_nano,
		u.billed_nano,
		u.margin_nano,
		u.cache_saved_nano,
		u.errors,
		if(u.requests > 0, toFloat64(u.errors) / toFloat64(u.requests), 0.0) AS error_rate,
		u.active_users,
		u.active_orgs
	FROM (
		SELECT
			model,
			provider,
			countMerge(requests) AS requests,
			sumMerge(tokens) AS tokens,
			sumMerge(prompt_tokens) AS prompt_tokens,
			sumMerge(completion_tokens) AS completion_tokens,
			sumMerge(cache_read_tokens) AS cache_read_tokens,
			sumMerge(cost_nano) AS cost_nano,
			sumMerge(billed_nano) AS billed_nano,
			sumMerge(margin_nano) AS margin_nano,
			sumMerge(cache_saved_nano) AS cache_saved_nano,
			sumMerge(errors) AS errors,
			uniqMerge(users) AS active_users,
			uniqExact(org) AS active_orgs
		FROM analytics.rollup_ai_daily
		GROUP BY model, provider
	) AS u
	LEFT JOIN (
		SELECT
			model,
			argMax(family, updated_at) AS family
		FROM analytics.dim_models
		GROUP BY model
	) AS m USING (model)
	ORDER BY u.billed_nano DESC`,
	},
	{
		Name: "analytics.provider_economics",
		SQL: `CREATE OR REPLACE VIEW analytics.provider_economics AS
	SELECT
		provider,
		countMerge(requests) AS requests,
		sumMerge(tokens) AS tokens,
		sumMerge(cost_nano) AS cost_nano,
		sumMerge(billed_nano) AS billed_nano,
		sumMerge(margin_nano) AS margin_nano,
		if(billed_nano > 0, intDiv(margin_nano * 10000, billed_nano), 0) AS margin_bps,
		sumMerge(cache_saved_nano) AS cache_saved_nano,
		sumMerge(errors) AS errors,
		if(requests > 0, toFloat64(errors) / toFloat64(requests), 0.0) AS error_rate,
		uniqExact(model) AS models,
		uniqExact(org) AS active_orgs
	FROM analytics.rollup_ai_daily
	GROUP BY provider
	ORDER BY billed_nano DESC`,
	},
	{
		Name: "analytics.product_performance",
		SQL: `CREATE OR REPLACE VIEW analytics.product_performance AS
	SELECT
		p.product AS product,
		coalesce(d.category, '') AS category,
		coalesce(d.owner_team, '') AS owner_team,
		p.events,
		p.active_users,
		p.sessions,
		p.bytes,
		p.active_orgs,
		coalesce(c.revenue_30d_minor, 0) AS revenue_30d_minor
	FROM (
		SELECT
			product,
			countMerge(events) AS events,
			uniqMerge(users) AS active_users,
			uniqMerge(sessions) AS sessions,
			sumMerge(bytes) AS bytes,
			uniqExact(org) AS active_orgs
		FROM analytics.rollup_product_hourly
		GROUP BY product
	) AS p
	LEFT JOIN (
		SELECT
			product,
			argMax(category, updated_at) AS category,
			argMax(owner_team, updated_at) AS owner_team
		FROM analytics.dim_products
		GROUP BY product
	) AS d USING (product)
	LEFT JOIN (
		SELECT
			product,
			sumIf(amount_minor, kind IN ('usage', 'invoice_paid')) AS revenue_30d_minor
		FROM analytics.rollup_commerce_daily
		WHERE day >= today() - 30
		GROUP BY product
	) AS c USING (product)
	ORDER BY p.events DESC`,
	},
}

var analyticsSchemaReady atomic.Bool

// AnalyticsTableDDLs returns a copy of all canonical analytics table DDL descriptors.
func AnalyticsTableDDLs() []AnalyticsDDL {
	out := make([]AnalyticsDDL, len(analyticsTableDDLs))
	copy(out, analyticsTableDDLs)
	return out
}

// AnalyticsMVDDLs returns a copy of all canonical analytics materialized view DDL descriptors.
func AnalyticsMVDDLs() []AnalyticsDDL {
	out := make([]AnalyticsDDL, len(analyticsMVDDLs))
	copy(out, analyticsMVDDLs)
	return out
}

// AnalyticsViewDDLs returns a copy of all canonical analytics serving view DDL descriptors.
func AnalyticsViewDDLs() []AnalyticsDDL {
	out := make([]AnalyticsDDL, len(analyticsViewDDLs))
	copy(out, analyticsViewDDLs)
	return out
}

func tableExists(ctx context.Context, db, table string) (bool, error) {
	rows, err := Query(ctx, `SELECT count() AS c FROM system.tables WHERE database = ? AND name = ?`, db, table)
	if err != nil {
		return false, err
	}
	if len(rows) == 0 {
		return false, nil
	}
	return dsUint64(rows[0]["c"]) > 0, nil
}

// EnsureAnalyticsSchema idempotently creates the `analytics` database, canonical tables,
// incremental materialized views (guarded on external source table existence), and
// constant-cost serving views. Latches only on full success so transient boot errors retry.
func EnsureAnalyticsSchema(ctx context.Context) error {
	if analyticsSchemaReady.Load() {
		return nil
	}
	if !Ready() {
		return fmt.Errorf("datastore not connected")
	}
	if err := EnsureCloudUsage(ctx); err != nil {
		return fmt.Errorf("ensure cloud_usage: %w", err)
	}
	if err := Exec(ctx, createAnalyticsDB); err != nil {
		return fmt.Errorf("ensure analytics database: %w", err)
	}
	for _, ddl := range analyticsTableDDLs {
		if err := Exec(ctx, ddl.SQL); err != nil {
			return fmt.Errorf("ensure table %s: %w", ddl.Name, err)
		}
	}
	for _, mv := range analyticsMVDDLs {
		if mv.SourceDB != "" && mv.SourceDB != analyticsDatabase && mv.SourceDB != "hanzo" {
			ok, err := tableExists(ctx, mv.SourceDB, mv.SourceName)
			if err != nil {
				return fmt.Errorf("check source %s.%s for %s: %w", mv.SourceDB, mv.SourceName, mv.Name, err)
			}
			if !ok {
				continue
			}
		}
		if err := Exec(ctx, mv.SQL); err != nil {
			return fmt.Errorf("ensure mv %s: %w", mv.Name, err)
		}
	}
	for _, v := range analyticsViewDDLs {
		if err := Exec(ctx, v.SQL); err != nil {
			return fmt.Errorf("ensure view %s: %w", v.Name, err)
		}
	}
	analyticsSchemaReady.Store(true)
	return nil
}
