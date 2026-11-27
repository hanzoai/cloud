# Generated from typed ops. Do not edit.
# Every struct and method below is derived from one op's In and Out, and
# every offset from the layout the op-call plane encodes against.

package analytics

struct AnalyticsOverviewOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct FreshnessOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct HealthOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct MetricsIn {
    Domain text @0
}

struct MetricsOut {
    Status text  @0
    Msg    text  @8
    Data   bytes @16
}

struct OverviewIn {
    Org    text @0
    Window text @8
}

struct QueryIn {
    Metrics     list<text> @0
    GroupBy     list<text> @8
    Window      text       @16
    Granularity text       @24
    Org         text       @32
    Limit       i64        @40
}

interface analytics {
    # Reports per-pipeline watermarks, lag_ms, rows processed, and financial
    # reconciliation drift from analytics.watermarks, accelerated by Hanzo KV with stale-on-outage fallback.
    analyticsFreshness() returns (rep: FreshnessOut)
    # Reports Datastore reachability, active MergeTree parts and merge pressure,
    # Hanzo KV cache status, bounded query semaphore utilization, and active SSE stream counts.
    analyticsHealth() returns (rep: HealthOut)
    # Lists all canonical registered metrics, their units, allowed group_by dimensions,
    # upstream sources, freshness targets, accuracy classifications, and backing tables.
    analyticsMetrics(req: MetricsIn) returns (rep: MetricsOut)
    # Returns constant-cost global (SuperAdmin) or org-scoped executive KPIs
    # directly from analytics.global_overview / analytics.org_overview with zero per-org RPCs,
    # accelerated by Hanzo KV and degrading gracefully to the last-known KV snapshot if Datastore is unavailable.
    analyticsOverview(req: OverviewIn) returns (rep: AnalyticsOverviewOut)
}

# ---------------------------------------------------------------------
# 4 op(s) here. What follows is what this schema does not carry.
#
# blocked (1) — the op is absent; the field has no wire form:
#   analyticsQuery  QueryOut.Data  analytics.QueryData  (reaches one)
#
# opaque (4) — crosses, arrives without its name:
#   AnalyticsOverviewOut.Data  analytics.AnalyticsOverviewData
#   FreshnessOut.Data  analytics.FreshnessData
#   HealthOut.Data  analytics.HealthData
#   MetricsOut.Data  analytics.MetricsData
