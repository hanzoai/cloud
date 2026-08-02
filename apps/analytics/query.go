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

	"github.com/hanzoai/types"
)

// Warehouse + tables. The LLM ledger stays cloud's own; the product-event lenses
// read the EVENT PLANE — the same tables the write core's facts land in, their
// names derived from the one signal constant (fact.go) so transport, storage and
// lens cannot drift. The plane's DDL owner is hanzoai/o11y: cloud reads and writes
// rows, never creates tables, and answers honest-empty where the plane is absent.
const llmTable = "hanzo.cloud_usage" // live LLM usage ledger (real data today)

// The plane is ONE table now, so a lens does not pick a TABLE — it picks a SIGNAL, and
// the signal is a bound predicate like the tenant. `scope` below is where both are
// said, once, so a read cannot name one and forget the other.

// ── Tenancy predicates (the isolation boundary) ─────────────────────────────
//
// Both builders bind the org POSITIONALLY. The time bounds are bound too (as
// datastore DateTime string literals, the proven cloud_usage.go transport), so
// NOTHING user-derived is ever interpolated. cloud_usage keys the tenant on
// `organization`; the event plane keys it on `org` (== the IAM org slug).

// llmWhere is the org-scoped time predicate for hanzo.cloud_usage. org is the
// validated IAM owner slug, passed EXACTLY (the ledger stored it verbatim); it is
// always the trailing bound parameter.
func llmWhere(org string, start, end time.Time) (string, []any) {
	return "timestamp >= ? AND timestamp < ? AND organization = ?",
		[]any{tsLiteral(start), tsLiteral(end), org}
}

// scope is the MANDATORY leading predicate of EVERY read on the event plane: the
// tenant first, then the signal, both BOUND. It exists because those two are one
// decision — "whose rows, and which sort" — and a read that named a table used to
// answer the second by accident. One table means the signal is now a predicate, and a
// predicate you can forget is a lens that silently reads logs as product events.
//
// org leads because it leads the sort key: (org, time, id). signal follows because it
// leads the PARTITION key, so the pair prunes to one tenant's slice of one signal
// before any narrower is considered.
func scope(org string, sig signal) (string, []any) {
	return "org = ? AND signal = ?", []any{org, string(sig)}
}

// eventsWhere is the org-scoped, act-scoped time predicate for the behavior lenses.
// Same shape as llmWhere but keyed on the plane's own columns: `time` (DateTime64) and
// `org` (the IAM org slug, stamped server-side by normalize).
func eventsWhere(org string, start, end time.Time) (string, []any) {
	where, args := scope(org, signalAct)
	return where + " AND time >= ? AND time < ?",
		append(args, tsLiteral(start), tsLiteral(end))
}

// Behavior-lens group-key expressions. Each is a SERVER-CHOSEN constant SQL
// expression (never user input), so interpolating it into breakdownSQL is
// injection-safe — the org + time bounds stay bound parameters via eventsWhere.
// referrer/utm_* live in the envelope's attributes Map — same facts, same names,
// one map access instead of a dedicated column (the plane's shape; see fact.go
// attributesOf).
//   - pageKeyExpr: the requested path ("where people go / what they look at").
//   - referrerKeyExpr: the external referrer domain, falling back to the raw
//     referrer when the domain didn't parse; a missing referrer OR a same-origin
//     one (referrer_domain == domain(url), i.e. a self-referral) buckets to
//     "(direct)" — the origin/referral split callers expect.
//   - sourceKeyExpr: utm_source, with empty campaigns bucketed to "(none)".
const (
	pageKeyExpr     = "path"
	referrerKeyExpr = "multiIf(attributes['referrer_domain'] != '' AND attributes['referrer_domain'] != domain(url), attributes['referrer_domain'], attributes['referrer_domain'] = '' AND attributes['referrer'] != '', attributes['referrer'], '(direct)')"
	sourceKeyExpr   = "if(attributes['utm_source'] != '', attributes['utm_source'], '(none)')"
)

// breakdownSQL builds ONE pageview breakdown over event.fact grouped by keyExpr
// — the read core of the behavior lenses (topPages/topReferrers/topSources). Each
// returned bucket carries its pageviews (count of kind='page' rows — the plane's
// discriminator, which replaced the magic '$pageview' name) and visitors
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
			"FROM %s WHERE %s AND kind = 'page' GROUP BY k"+
			") ORDER BY pageviews DESC, visitors DESC LIMIT %d",
		keyExpr, factTable, where, limit)
	return sql, args
}

// tsLiteral formats a time as a datastore DateTime literal (UTC). Bound as a
// string arg — identical to ai/object/cloud_usage.go's cloudUsageTS.
func tsLiteral(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }

// ── Response types ──────────────────────────────────────────────────────────

// Scope names WHOSE data a lens answered with — the tenant the server resolved, so a
// reader can see the answer is its own org's and not a parameter it passed.
type Scope struct {
	// Org is the IAM org slug the rows were read under: the validated principal's,
	// resolved server-side.
	Org string `json:"org"`
}

// LLMOverview is the flagship lens: real per-org KPIs from hanzo.cloud_usage.
type LLMOverview struct {
	// Available is true whenever the ledger answered — including with no usage in the
	// window, which is honest zeros rather than a missing lens.
	Available bool `json:"available"`
	// Requests is how many LLM calls the org made in the window.
	Requests int64 `json:"requests"`
	// Tokens is prompt plus completion tokens over those calls.
	Tokens int64 `json:"tokens"`
	// PromptTokens is the input half of Tokens.
	PromptTokens int64 `json:"promptTokens"`
	// CompletionTokens is the output half of Tokens.
	CompletionTokens int64 `json:"completionTokens"`
	// SpendCents is what those calls cost, in cents.
	SpendCents int64 `json:"spendCents"`
	// Models is how many distinct models the org called.
	Models int64 `json:"models"`
	// Providers is how many distinct providers served them.
	Providers int64 `json:"providers"`
	// Errors is how many of Requests failed.
	Errors int64 `json:"errors"`
	// ErrorRate is Errors/Requests, 0..1, rounded to three places. Zero when there
	// were no requests.
	ErrorRate float64 `json:"errorRate"`
	// Source is the warehouse table the lens read.
	Source string `json:"source"`
}

// WebOverview is the web lens over event.fact. Honest-empty (Available=false)
// until the collector emits web events.
type WebOverview struct {
	// Available is false when the product-event table could not be read — the lens is
	// reported missing rather than as zeros that look like real traffic.
	Available bool `json:"available"`
	// Reason says why the lens is unavailable. Omitted when it is available.
	Reason string `json:"reason,omitempty"`
	// Pageviews is how many $pageview events landed in the window.
	Pageviews int64 `json:"pageviews"`
	// Visitors is how many distinct people those pageviews came from.
	Visitors int64 `json:"visitors"`
	// Sessions is how many distinct visits they span.
	Sessions int64 `json:"sessions"`
	// Source is the warehouse table the lens read.
	Source string `json:"source"`
}

// CommerceOverview is the commerce lens over event.fact. Honest-empty until
// commerce emits order events.
type CommerceOverview struct {
	// Available is false when the product-event table could not be read — the lens is
	// reported missing rather than as zeros that look like no sales.
	Available bool `json:"available"`
	// Reason says why the lens is unavailable. Omitted when it is available.
	Reason string `json:"reason,omitempty"`
	// Orders is how many order_completed events landed in the window.
	Orders int64 `json:"orders"`
	// Revenue is the total those orders carried, in the events' own currency unit.
	Revenue float64 `json:"revenue"`
	// AOV is average order value — Revenue/Orders, rounded to two places. Zero when
	// there were no orders.
	AOV float64 `json:"aov"`
	// Source is the warehouse table the lens read.
	Source string `json:"source"`
}

// Overview is one window's KPIs across all three lenses — the console's landing view.
type Overview struct {
	// Range is the window that was actually applied: 24h, 7d, 30d or custom.
	Range string `json:"range"`
	// Start is the window's inclusive lower bound, RFC3339 UTC.
	Start string `json:"start"`
	// End is the window's exclusive upper bound, RFC3339 UTC.
	End string `json:"end"`
	// Interval is the bucket width the window implies: hour or day.
	Interval types.Interval `json:"interval"`
	// Scope names the tenant these numbers belong to.
	Scope Scope `json:"scope"`
	// LLM is the LLM usage lens — real per-org data.
	LLM LLMOverview `json:"llm"`
	// Web is the web-traffic lens over product events.
	Web WebOverview `json:"web"`
	// Commerce is the orders/revenue lens over product events.
	Commerce CommerceOverview `json:"commerce"`
}

// UsagePoint is one bucket of the LLM usage series. Named for the value rather than
// for its shape: the OpenAPI schema namespace is FLAT across the whole fleet, and
// `SeriesPoint` is already the admin launch board's {t, value} pair — one name with
// two shapes is what the fleet weave refuses, since every generated SDK would bind
// whichever it read last.
type UsagePoint struct {
	// T is the bucket's start, RFC3339 UTC, aligned to the interval.
	T string `json:"t"`
	// Requests is how many LLM calls fell in this bucket.
	Requests int64 `json:"requests"`
	// Tokens is prompt plus completion tokens over those calls.
	Tokens int64 `json:"tokens"`
	// SpendCents is what they cost, in cents.
	SpendCents int64 `json:"spendCents"`
}

// Timeseries is the LLM usage series over one window, gap-filled so a client charts a
// continuous line.
type Timeseries struct {
	// Range is the window that was actually applied: 24h, 7d, 30d or custom.
	Range string `json:"range"`
	// Start is the window's inclusive lower bound, RFC3339 UTC.
	Start string `json:"start"`
	// End is the window's exclusive upper bound, RFC3339 UTC.
	End string `json:"end"`
	// Interval is the bucket width: hour or day.
	Interval types.Interval `json:"interval"`
	// Scope names the tenant these numbers belong to.
	Scope Scope `json:"scope"`
	// Series is one point per bucket, oldest first, with empty buckets zero-filled.
	Series []UsagePoint `json:"series"`
	// Source is the warehouse table the series read.
	Source string `json:"source"`
}

// ModelRow is one model's usage in the window, ranked by spend.
type ModelRow struct {
	// Model is the model id, e.g. zen5-coder.
	Model string `json:"model"`
	// Provider is who served it.
	Provider string `json:"provider"`
	// Requests is how many calls went to this model.
	Requests int64 `json:"requests"`
	// Tokens is prompt plus completion tokens over those calls.
	Tokens int64 `json:"tokens"`
	// SpendCents is what they cost, in cents.
	SpendCents int64 `json:"spendCents"`
	// Pct is this model's share of the window's returned spend, 0..100, one decimal.
	Pct float64 `json:"pct"`
}

// TopModels is the models lens: the window's models ranked by spend, then requests.
type TopModels struct {
	// Available is true whenever the ledger answered, including with no rows.
	Available bool `json:"available"`
	// Items is the ranked models, highest spend first.
	Items []ModelRow `json:"items"`
	// Source is the warehouse table the lens read.
	Source string `json:"source"`
}

// ProductRow is one product's commerce result in the window, ranked by revenue.
type ProductRow struct {
	// ProductID is the product the order events named.
	ProductID string `json:"productId"`
	// Orders is how many order_completed events carried it.
	Orders int64 `json:"orders"`
	// Revenue is the total they carried, in the events' own currency unit.
	Revenue float64 `json:"revenue"`
	// Units is the summed quantity sold.
	Units int64 `json:"units"`
}

// TopProducts is the products lens over product events. Honest-empty until commerce
// emits order events.
type TopProducts struct {
	// Available is false when the product-event table could not be read.
	Available bool `json:"available"`
	// Reason says why the lens is unavailable. Omitted when it is available.
	Reason string `json:"reason,omitempty"`
	// Items is the ranked products, highest revenue first. Empty rather than absent.
	Items []ProductRow `json:"items"`
	// Source is the warehouse table the lens read.
	Source string `json:"source"`
}

// BreakdownRow is one bucket of a behavior lens (a path, a referrer domain, a
// utm source). pct is the bucket's share of TOTAL pageviews in-window (the
// window-fn denominator), so a top-N list honestly shows the long tail rather
// than re-normalizing to the shown rows.
type BreakdownRow struct {
	// Key is the bucket: a requested path, a referrer domain ("(direct)" for none or
	// a same-origin one), or a utm_source ("(none)" when absent).
	Key string `json:"key"`
	// Pageviews is how many $pageview events fell in this bucket.
	Pageviews int64 `json:"pageviews"`
	// Visitors is how many distinct people they came from.
	Visitors int64 `json:"visitors"`
	// Pct is this bucket's share of ALL in-window pageviews, 0..100, one decimal —
	// not of the returned rows, so a top-N shows the long tail honestly.
	Pct float64 `json:"pct"`
}

// Breakdown is a ranked behavior lens over event.fact. Honest-empty
// (Available=false) when the events table is absent/errored — never fabricated.
type Breakdown struct {
	// Available is false when the product-event table could not be read.
	Available bool `json:"available"`
	// Reason says why the lens is unavailable. Omitted when it is available.
	Reason string `json:"reason,omitempty"`
	// Items is the ranked buckets, most pageviews first. Empty rather than absent.
	Items []BreakdownRow `json:"items"`
	// Source is the warehouse table the lens read.
	Source string `json:"source"`
}

// Top is one window's five ranked lenses — the console's "what is driving this"
// view.
type Top struct {
	// Range is the window that was actually applied: 24h, 7d, 30d or custom.
	Range string `json:"range"`
	// Start is the window's inclusive lower bound, RFC3339 UTC.
	Start string `json:"start"`
	// End is the window's exclusive upper bound, RFC3339 UTC.
	End string `json:"end"`
	// Scope names the tenant these rankings belong to.
	Scope Scope `json:"scope"`
	// Models ranks the window's LLM models by spend — real per-org data.
	Models TopModels `json:"models"`
	// Products ranks the window's products by revenue.
	Products TopProducts `json:"products"`
	// Pages ranks the paths visitors requested, by pageviews.
	Pages Breakdown `json:"topPages"`
	// Referrers ranks the external domains visitors arrived from, by pageviews.
	Referrers Breakdown `json:"topReferrers"`
	// Sources ranks the utm_source campaigns visitors arrived on, by pageviews.
	Sources Breakdown `json:"topSources"`
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
	w := WebOverview{Available: ok, Source: factTable}
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
	c := CommerceOverview{Available: ok, Source: factTable}
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
func buildSeries(start, end time.Time, interval types.Interval, rows []map[string]any) []UsagePoint {
	step := interval.Step()

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

	out := make([]UsagePoint, 0, 64)
	for t := start.UTC().Truncate(step); t.Before(end); t = t.Add(step) {
		a := idx[t.Unix()]
		out = append(out, UsagePoint{
			T:          t.UTC().Format(time.RFC3339),
			Requests:   a.requests,
			Tokens:     a.tokens,
			SpendCents: a.spend,
		})
	}
	return out
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

// buildTopProducts assembles the top-products table from event.fact. ok=false
// (events table absent) → honest-empty. Pure.
func buildTopProducts(rows []map[string]any, ok bool) TopProducts {
	if !ok {
		return TopProducts{Available: false, Reason: "no commerce events yet", Items: []ProductRow{}, Source: factTable}
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
	return TopProducts{Available: true, Items: items, Source: factTable}
}

// buildBreakdown assembles a behavior lens from breakdownSQL rows. ok=false
// (events table absent/errored) → honest-empty with a non-nil empty list, exactly
// like buildTopProducts. pct is each bucket's share of the in-window pageview total
// carried by the window-fn `total` column (identical on every row); an empty
// result yields total 0 → all pct 0. Pure — the tests drive it with mock rows.
func buildBreakdown(rows []map[string]any, ok bool) Breakdown {
	if !ok {
		return Breakdown{Available: false, Reason: "no web analytics events yet", Items: []BreakdownRow{}, Source: factTable}
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
	return Breakdown{Available: true, Items: items, Source: factTable}
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

// aStrMap coerces an attributes Map column across the shapes the transports
// surface: the native driver's map[string]string, a JSON-transport map[string]any,
// or a JSON object serialized to text. nil for anything else — the lenses treat
// that as "no attributes", never an error.
func aStrMap(v any) map[string]string {
	switch m := v.(type) {
	case nil:
		return nil
	case map[string]string:
		return m
	case map[string]any:
		out := make(map[string]string, len(m))
		for k, e := range m {
			out[k] = asStr(e)
		}
		return out
	case string:
		var out map[string]string
		if json.Unmarshal([]byte(m), &out) == nil {
			return out
		}
		return nil
	default:
		return nil
	}
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
