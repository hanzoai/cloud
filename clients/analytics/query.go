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

// Pure core of the analytics lens: SQL predicate builders, datastore value
// coercers, and the pure assemblers that turn raw datastore rows into the
// response structs. Everything here is I/O-free so the tests drive it with mock
// rows — no datastore needed — exactly as ai/object/cloud_usage.go proves out
// its Overview assembler. The handlers (analytics.go) are the thin orchestration
// that fetches the rows and calls these.
//
// THE ONE TENANCY INVARIANT lives here: llmWhere / eventsWhere ALWAYS emit
// "… = ?" with the org bound POSITIONALLY (never interpolated), so no query this
// package builds can read a tenant other than the caller's, and a hostile org
// slug can never escape into SQL. The isolation test asserts this directly.
package analytics

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Warehouse + tables (the ONE analytics warehouse per unified-analytics.md §1).
const (
	llmTable    = "hanzo.cloud_usage" // live LLM usage ledger (real data today)
	eventsTable = "hanzo.events"      // web/commerce/UI wide event table (honest-empty until the collector emits)
)

// ── Tenancy predicates (the isolation boundary) ─────────────────────────────
//
// Both builders bind the org POSITIONALLY. The time bounds are bound too (as
// datastore DateTime string literals, the proven cloud_usage.go transport), so
// NOTHING user-derived is ever interpolated. cloud_usage keys the tenant on
// `organization`; hanzo.events keys it on `tenant_id` (== the IAM org slug).

// llmWhere is the org-scoped time predicate for hanzo.cloud_usage. org is the
// validated IAM owner slug, passed EXACTLY (the ledger stored it verbatim); it is
// always the trailing bound parameter.
func llmWhere(org string, start, end time.Time) (string, []any) {
	return "timestamp >= ? AND timestamp < ? AND organization = ?",
		[]any{tsLiteral(start), tsLiteral(end), org}
}

// eventsWhere is the org-scoped time predicate for hanzo.events. Same shape as
// llmWhere but keyed on `tenant_id` (the events table's canonical org column).
func eventsWhere(org string, start, end time.Time) (string, []any) {
	return "timestamp >= ? AND timestamp < ? AND tenant_id = ?",
		[]any{tsLiteral(start), tsLiteral(end), org}
}

// Behavior-lens group-key expressions. Each is a SERVER-CHOSEN constant SQL
// expression (never user input), so interpolating it into breakdownSQL is
// injection-safe — the org + time bounds stay bound parameters via eventsWhere.
//   - pageKeyExpr: the requested path ("where people go / what they look at").
//   - referrerKeyExpr: the external referrer domain, falling back to the raw
//     referrer when the domain didn't parse; a missing referrer OR a same-origin
//     one (referrer_domain == domain(url), i.e. a self-referral) buckets to
//     "(direct)" — the origin/referral split callers expect.
//   - sourceKeyExpr: utm_source, with empty campaigns bucketed to "(none)".
const (
	pageKeyExpr     = "path"
	referrerKeyExpr = "multiIf(referrer_domain != '' AND referrer_domain != domain(url), referrer_domain, referrer_domain = '' AND referrer != '', referrer, '(direct)')"
	sourceKeyExpr   = "if(utm_source != '', utm_source, '(none)')"
)

// breakdownSQL builds ONE pageview breakdown over hanzo.events grouped by keyExpr
// — the read core of the behavior lenses (topPages/topReferrers/topSources). Each
// returned bucket carries its pageviews (count of $pageview rows) and visitors
// (uniqExact distinct_id), plus the in-window pageview grand total via
// `sum(pageviews) OVER ()`: because every pageview maps to exactly one bucket, that
// window total is the TRUE total pageviews in-window, so buildBreakdown's pct is an
// honest share of the whole (not merely of the returned top-N).
//
// Tenancy: keyExpr is a package constant (never user input) and limit is a
// validated int, so the only user-derived values — org + time bounds — stay bound
// parameters through eventsWhere. The isolation boundary is identical to every
// other query this package builds.
func breakdownSQL(keyExpr, org string, start, end time.Time, limit int) (string, []any) {
	where, args := eventsWhere(org, start, end)
	sql := fmt.Sprintf(
		"SELECT k, pageviews, visitors, sum(pageviews) OVER () AS total FROM ("+
			"SELECT %s AS k, count() AS pageviews, uniqExact(distinct_id) AS visitors "+
			"FROM %s WHERE %s AND event = '$pageview' GROUP BY k"+
			") ORDER BY pageviews DESC, visitors DESC LIMIT %d",
		keyExpr, eventsTable, where, limit)
	return sql, args
}

// tsLiteral formats a time as a datastore DateTime literal (UTC). Bound as a
// string arg — identical to ai/object/cloud_usage.go's cloudUsageTS.
func tsLiteral(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }

// ── Response types ──────────────────────────────────────────────────────────

type Scope struct {
	Org string `json:"org"`
}

// LLMOverview is the flagship lens: real per-org KPIs from hanzo.cloud_usage.
type LLMOverview struct {
	Available        bool    `json:"available"`
	Requests         int64   `json:"requests"`
	Tokens           int64   `json:"tokens"`
	PromptTokens     int64   `json:"promptTokens"`
	CompletionTokens int64   `json:"completionTokens"`
	SpendCents       int64   `json:"spendCents"`
	Models           int64   `json:"models"`
	Providers        int64   `json:"providers"`
	Errors           int64   `json:"errors"`
	ErrorRate        float64 `json:"errorRate"` // 0..1, errors/requests
	Source           string  `json:"source"`
}

// WebOverview is the web lens over hanzo.events. Honest-empty (Available=false)
// until the collector emits web events.
type WebOverview struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Pageviews int64  `json:"pageviews"`
	Visitors  int64  `json:"visitors"`
	Sessions  int64  `json:"sessions"`
	Source    string `json:"source"`
}

// CommerceOverview is the commerce lens over hanzo.events. Honest-empty until
// commerce emits order events.
type CommerceOverview struct {
	Available bool    `json:"available"`
	Reason    string  `json:"reason,omitempty"`
	Orders    int64   `json:"orders"`
	Revenue   float64 `json:"revenue"`
	AOV       float64 `json:"aov"` // revenue/orders
	Source    string  `json:"source"`
}

type Overview struct {
	Range    string           `json:"range"`
	Start    string           `json:"start"`
	End      string           `json:"end"`
	Interval string           `json:"interval"`
	Scope    Scope            `json:"scope"`
	LLM      LLMOverview      `json:"llm"`
	Web      WebOverview      `json:"web"`
	Commerce CommerceOverview `json:"commerce"`
}

type SeriesPoint struct {
	T          string `json:"t"` // RFC3339 bucket start (UTC)
	Requests   int64  `json:"requests"`
	Tokens     int64  `json:"tokens"`
	SpendCents int64  `json:"spendCents"`
}

type Timeseries struct {
	Range    string        `json:"range"`
	Start    string        `json:"start"`
	End      string        `json:"end"`
	Interval string        `json:"interval"`
	Scope    Scope         `json:"scope"`
	Series   []SeriesPoint `json:"series"`
	Source   string        `json:"source"`
}

type ModelRow struct {
	Model      string  `json:"model"`
	Provider   string  `json:"provider"`
	Requests   int64   `json:"requests"`
	Tokens     int64   `json:"tokens"`
	SpendCents int64   `json:"spendCents"`
	Pct        float64 `json:"pct"` // share of total spend, 0..100
}

type TopModels struct {
	Available bool       `json:"available"`
	Items     []ModelRow `json:"items"`
	Source    string     `json:"source"`
}

type ProductRow struct {
	ProductID string  `json:"productId"`
	Orders    int64   `json:"orders"`
	Revenue   float64 `json:"revenue"`
	Units     int64   `json:"units"`
}

type TopProducts struct {
	Available bool         `json:"available"`
	Reason    string       `json:"reason,omitempty"`
	Items     []ProductRow `json:"items"`
	Source    string       `json:"source"`
}

// BreakdownRow is one bucket of a behavior lens (a path, a referrer domain, a
// utm source). pct is the bucket's share of TOTAL pageviews in-window (the
// window-fn denominator), so a top-N list honestly shows the long tail rather
// than re-normalizing to the shown rows.
type BreakdownRow struct {
	Key       string  `json:"key"`
	Pageviews int64   `json:"pageviews"`
	Visitors  int64   `json:"visitors"`
	Pct       float64 `json:"pct"` // share of total pageviews in-window, 0..100
}

// Breakdown is a ranked behavior lens over hanzo.events. Honest-empty
// (Available=false) when the events table is absent/errored — never fabricated.
type Breakdown struct {
	Available bool           `json:"available"`
	Reason    string         `json:"reason,omitempty"`
	Items     []BreakdownRow `json:"items"`
	Source    string         `json:"source"`
}

type Top struct {
	Range    string      `json:"range"`
	Start    string      `json:"start"`
	End      string      `json:"end"`
	Scope    Scope       `json:"scope"`
	Models   TopModels   `json:"models"`
	Products TopProducts `json:"products"`
	// Behavior lenses (events lens): WHERE people go / WHAT they look at
	// (topPages) and where they come FROM (topReferrers organic/referral,
	// topSources campaigns). Honest-empty until the beacon fills hanzo.events.
	Pages     Breakdown `json:"topPages"`
	Referrers Breakdown `json:"topReferrers"`
	Sources   Breakdown `json:"topSources"`
}

// ── Pure assemblers ─────────────────────────────────────────────────────────

// buildLLMOverview assembles the LLM KPI block from the single aggregate row.
// A nil/empty row yields honest zeros (Available is still true — the datastore
// answered; there is simply no usage in the window). Pure.
func buildLLMOverview(row map[string]any) LLMOverview {
	requests := aInt64(row["requests"])
	errors := aInt64(row["errors"])
	o := LLMOverview{
		Available:        true,
		Requests:         requests,
		Tokens:           aInt64(row["tokens"]),
		PromptTokens:     aInt64(row["prompt_tokens"]),
		CompletionTokens: aInt64(row["completion_tokens"]),
		SpendCents:       aInt64(row["cost_cents"]),
		Models:           aInt64(row["models"]),
		Providers:        aInt64(row["providers"]),
		Errors:           errors,
		Source:           llmTable,
	}
	if requests > 0 {
		o.ErrorRate = round3(float64(errors) / float64(requests))
	}
	return o
}

// buildWebOverview / buildCommerceOverview assemble the events lenses. The
// handler passes ok=false when the events query failed (table absent) so the
// lens is honestly reported unavailable rather than as fabricated zeros.
func buildWebOverview(row map[string]any, ok bool) WebOverview {
	w := WebOverview{Available: ok, Source: eventsTable}
	if !ok {
		w.Reason = "no web analytics events yet"
		return w
	}
	w.Pageviews = aInt64(row["pageviews"])
	w.Visitors = aInt64(row["visitors"])
	w.Sessions = aInt64(row["sessions"])
	return w
}

func buildCommerceOverview(row map[string]any, ok bool) CommerceOverview {
	c := CommerceOverview{Available: ok, Source: eventsTable}
	if !ok {
		c.Reason = "no commerce events yet"
		return c
	}
	c.Orders = aInt64(row["orders"])
	c.Revenue = aFloat64(row["revenue"])
	if c.Orders > 0 {
		c.AOV = round2(c.Revenue / float64(c.Orders))
	}
	return c
}

// buildSeries turns sparse datastore buckets into an evenly-spaced, gap-filled
// series so the client charts a continuous line. Bucket alignment matches
// toStartOf{Hour,Day}(…, 'UTC'): Go's Truncate over the step lands on the same
// UTC boundaries. Pure (mirrors ai/object buildCloudUsageSeries).
func buildSeries(start, end time.Time, interval string, rows []map[string]any) []SeriesPoint {
	step := stepOf(interval)

	type agg struct{ requests, tokens, spend int64 }
	idx := make(map[int64]agg, len(rows))
	for _, r := range rows {
		bt := aTime(r["bucket"]).Truncate(step)
		idx[bt.Unix()] = agg{
			requests: aInt64(r["requests"]),
			tokens:   aInt64(r["tokens"]),
			spend:    aInt64(r["cost_cents"]),
		}
	}

	out := make([]SeriesPoint, 0, 64)
	for t := start.UTC().Truncate(step); t.Before(end); t = t.Add(step) {
		a := idx[t.Unix()]
		out = append(out, SeriesPoint{
			T:          t.UTC().Format(time.RFC3339),
			Requests:   a.requests,
			Tokens:     a.tokens,
			SpendCents: a.spend,
		})
	}
	return out
}

func stepOf(interval string) time.Duration {
	if strings.EqualFold(interval, "day") {
		return 24 * time.Hour
	}
	return time.Hour
}

// buildTopModels assembles the top-models table, computing each model's share of
// total spend. Rows arrive already ordered by the query, but we sort defensively
// so the pct/ordering is correct regardless of driver row order. Pure.
func buildTopModels(rows []map[string]any) TopModels {
	items := make([]ModelRow, 0, len(rows))
	var totalCents int64
	for _, r := range rows {
		spend := aInt64(r["cost_cents"])
		totalCents += spend
		items = append(items, ModelRow{
			Model:      aString(r["model"]),
			Provider:   aString(r["provider"]),
			Requests:   aInt64(r["requests"]),
			Tokens:     aInt64(r["tokens"]),
			SpendCents: spend,
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].SpendCents != items[j].SpendCents {
			return items[i].SpendCents > items[j].SpendCents
		}
		return items[i].Requests > items[j].Requests
	})
	for i := range items {
		items[i].Pct = pctOf(items[i].SpendCents, totalCents)
	}
	return TopModels{Available: true, Items: items, Source: llmTable}
}

// buildTopProducts assembles the top-products table from hanzo.events. ok=false
// (events table absent) → honest-empty. Pure.
func buildTopProducts(rows []map[string]any, ok bool) TopProducts {
	if !ok {
		return TopProducts{Available: false, Reason: "no commerce events yet", Items: []ProductRow{}, Source: eventsTable}
	}
	items := make([]ProductRow, 0, len(rows))
	for _, r := range rows {
		items = append(items, ProductRow{
			ProductID: aString(r["productId"]),
			Orders:    aInt64(r["orders"]),
			Revenue:   aFloat64(r["revenue"]),
			Units:     aInt64(r["units"]),
		})
	}
	return TopProducts{Available: true, Items: items, Source: eventsTable}
}

// buildBreakdown assembles a behavior lens from breakdownSQL rows. ok=false
// (events table absent/errored) → honest-empty with a non-nil empty list, exactly
// like buildTopProducts. pct is each bucket's share of the in-window pageview total
// carried by the window-fn `total` column (identical on every row); an empty
// result yields total 0 → all pct 0. Pure — the tests drive it with mock rows.
func buildBreakdown(rows []map[string]any, ok bool) Breakdown {
	if !ok {
		return Breakdown{Available: false, Reason: "no web analytics events yet", Items: []BreakdownRow{}, Source: eventsTable}
	}
	var total int64
	if len(rows) > 0 {
		total = aInt64(rows[0]["total"])
	}
	items := make([]BreakdownRow, 0, len(rows))
	for _, r := range rows {
		items = append(items, BreakdownRow{
			Key:       aString(r["k"]),
			Pageviews: aInt64(r["pageviews"]),
			Visitors:  aInt64(r["visitors"]),
		})
	}
	for i := range items {
		items[i].Pct = pctOf(items[i].Pageviews, total)
	}
	return Breakdown{Available: true, Items: items, Source: eventsTable}
}

// ── Value coercion ──────────────────────────────────────────────────────────
//
// The direct datastore driver decodes each column to its native Go scan type
// (uint64 for count()/sum(UInt*), float64 for toFloat64, time.Time for DateTime,
// string for String). These coercers accept those natives AND the JSON-transport
// fallbacks (float64/json.Number/string) so a transport change can't crash a read.

func aInt64(v any) int64 {
	switch n := v.(type) {
	case nil:
		return 0
	case int:
		return int64(n)
	case int64:
		return n
	case int32:
		return int64(n)
	case uint:
		return int64(n)
	case uint64:
		return int64(n)
	case uint32:
		return int64(n)
	case uint16:
		return int64(n)
	case uint8:
		return int64(n)
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case string:
		if i, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
			return i
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(n), 64); err == nil {
			return int64(f)
		}
		return 0
	default:
		return 0
	}
}

func aFloat64(v any) float64 {
	switch n := v.(type) {
	case nil:
		return 0
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case uint64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return f
	default:
		return 0
	}
}

func aString(v any) string {
	switch s := v.(type) {
	case nil:
		return ""
	case string:
		return s
	case fmt.Stringer:
		return s.String()
	default:
		return fmt.Sprintf("%v", s)
	}
}

func aTime(v any) time.Time {
	if t, ok := v.(time.Time); ok {
		return t.UTC()
	}
	s := strings.TrimSpace(aString(v))
	if s != "" {
		for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339, "2006-01-02"} {
			if t, err := time.Parse(layout, s); err == nil {
				return t.UTC()
			}
		}
	}
	if n := aInt64(v); n > 0 {
		return time.Unix(n, 0).UTC()
	}
	return time.Time{}
}

func pctOf(part, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return round1(float64(part) / float64(total) * 100)
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }
func round2(f float64) float64 { return math.Round(f*100) / 100 }
func round3(f float64) float64 { return math.Round(f*1000) / 1000 }
