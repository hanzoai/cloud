// Package metrics is the fleet SaaS-operations god-view (/v1/admin/metrics) — the
// operator's business dashboard: MRR/ARR, net-new vs churned MRR, the plan/category
// mix, the top customers, and the recent subscription movements. SuperAdmin only
// (core.Guard).
//
// It reads the ONE shared warehouse (commerce.events) — the table the commerce
// analytics collector lands every subscription/invoice/usage-lifecycle event in —
// over the SAME client (aiobject.DatastoreQuery) the o11y/compute lenses use, with
// ZERO per-org fan-out. Each panel is ONE aggregate query that folds the whole fleet
// (subscription state = latest-event-wins via argMax; new/churn/usage = windowed),
// exactly the way o11y.go composes independent per-signal reads. An unconnected
// warehouse — or the collector's events table not provisioned yet — degrades to an
// honest empty snapshot (real zeros, `[]` not null) with a not-ok source, never a
// fabricated number. Money is USD cents end to end; time bounds are POSITIONAL args.
package metrics

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/admin/money"
	"github.com/zap-proto/zip"
)

// errUnconfigured marks the warehouse not connected on this deployment — core.SrcOf
// reports it as a not-ok source so the console renders the honest not-configured state.
var errUnconfigured = errors.New("billing warehouse not connected")

// defaultLimit caps the top-customers list; recentLimit caps the movement feed.
const (
	defaultLimit = 20
	recentLimit  = 20
)

// ── response shapes (byte-identical to the operator contract in api.ts) ──────
// These were formerly modeled on the commerce S2S client; they now live here (the
// one consumer) since the read is a direct warehouse aggregate. Money is money.Cents
// (int64 underlying → plain-integer JSON, unchanged on the wire).

// SaaSMetrics is the whole-business SaaS-operations aggregate.
type SaaSMetrics struct {
	AsOf      string         `json:"asOf"`
	Currency  string         `json:"currency"`
	Window    string         `json:"window"`
	Revenue   SaaSRevenue    `json:"revenue"`
	Subs      SaaSSubs       `json:"subscriptions"`
	Usage     SaaSUsage      `json:"usage"`
	Customers []SaaSCustomer `json:"customers"`
	Orgs      int            `json:"orgs"`
	Gaps      []string       `json:"gaps"`
}

// SaaSRevenue is the recurring-revenue headline (run-rate MRR/ARR + windowed movement).
type SaaSRevenue struct {
	MRRCents            money.Cents    `json:"mrrCents"`
	ARRCents            money.Cents    `json:"arrCents"`
	ActiveSubscriptions int            `json:"activeSubscriptions"`
	PayingCustomers     int            `json:"payingCustomers"`
	Trials              int            `json:"trials"`
	NewMRRCents         money.Cents    `json:"newMrrCents"`
	ChurnedMRRCents     money.Cents    `json:"churnedMrrCents"`
	NetNewMRRCents      money.Cents    `json:"netNewMrrCents"`
	ByCategory          []SaaSCategory `json:"byCategory"`
}

// SaaSCategory is one plan-category bucket of run-rate MRR (the plan mix).
type SaaSCategory struct {
	Category      string      `json:"category"`
	MRRCents      money.Cents `json:"mrrCents"`
	Subscriptions int         `json:"subscriptions"`
}

// SaaSSubs is the subscription-operations panel (per-plan mix, trials, new/canceled,
// recent movements).
type SaaSSubs struct {
	ByPlan       []SaaSPlan  `json:"byPlan"`
	TrialsActive int         `json:"trialsActive"`
	New          int         `json:"new"`
	Canceled     int         `json:"canceled"`
	Recent       []SaaSEvent `json:"recent"`
}

// SaaSPlan is one plan's active/trialing counts, seats, and MRR contribution.
type SaaSPlan struct {
	Plan     string      `json:"plan"`
	Name     string      `json:"name"`
	Category string      `json:"category"`
	Active   int         `json:"active"`
	Trialing int         `json:"trialing"`
	Seats    int         `json:"seats"`
	MRRCents money.Cents `json:"mrrCents"`
}

// SaaSEvent is one recent subscription movement ("created" or "canceled").
type SaaSEvent struct {
	At            string      `json:"at"`
	Org           string      `json:"org"`
	Type          string      `json:"type"`
	Plan          string      `json:"plan"`
	Category      string      `json:"category"`
	MRRDeltaCents money.Cents `json:"mrrDeltaCents"`
}

// SaaSUsage is the metered / pay-as-you-go revenue headline for the window.
type SaaSUsage struct {
	Instrumented     bool        `json:"instrumented"`
	WindowUsageCents money.Cents `json:"windowUsageCents"`
	Requests         int64       `json:"requests"`
}

// SaaSCustomer is one top customer by MRR + windowed usage.
type SaaSCustomer struct {
	Org        string      `json:"org"`
	Plan       string      `json:"plan"`
	Category   string      `json:"category"`
	Status     string      `json:"status"`
	MRRCents   money.Cents `json:"mrrCents"`
	UsageCents money.Cents `json:"usageCents"`
	Seats      int         `json:"seats"`
	Since      string      `json:"since,omitempty"`
}

// MetricsData is the GET /v1/admin/metrics payload: the SaaS snapshot, flat, plus the
// admin read time and the upstream freshness strip every god-view carries.
type MetricsData struct {
	SaaSMetrics
	GeneratedAt string              `json:"generatedAt"`
	Sources     []core.SourceStatus `json:"sources"`
}

// Metrics answers GET /v1/admin/metrics by aggregating commerce.events directly
// (fleet-wide, no per-org fan-out). SuperAdmin only.
//
//	GET /v1/admin/metrics?window=30d&limit=20
func Metrics(s *cloud.Service[core.State], c *zip.Ctx) error {
	ctx := c.Context()
	now := time.Now().UTC().Format(time.RFC3339)
	window := normalizeWindow(c.Query("window"))
	limit := parseLimit(c.Query("limit"))

	// Honest not-configured snapshot when the warehouse/collector table is absent.
	if !core.BillingEventsReady(ctx) {
		return core.OK(c, empty(now, window, core.SrcOf("billing-warehouse", errUnconfigured, 0, now)))
	}

	sinceTS := core.CHTimeLit(core.WarehouseSince(window))
	m := SaaSMetrics{AsOf: now, Currency: "usd", Window: window}

	// Revenue headline + plan-mix (run-rate, latest-event-wins over active subs).
	if rows, err := aiobject.DatastoreQuery(ctx, headlineSQL()); err == nil {
		fillHeadline(&m.Revenue, core.CHFirstRow(rows))
	}
	if rows, err := aiobject.DatastoreQuery(ctx, byCategorySQL()); err == nil {
		m.Revenue.ByCategory = byCategoryFromRows(rows)
	}
	if rows, err := aiobject.DatastoreQuery(ctx, byPlanSQL()); err == nil {
		m.Subs.ByPlan = byPlanFromRows(rows)
	}
	m.Subs.TrialsActive = m.Revenue.Trials

	// Windowed movement: new vs churned MRR + counts.
	if rows, err := aiobject.DatastoreQuery(ctx, movementSQL(), sinceTS); err == nil {
		r := core.CHFirstRow(rows)
		m.Revenue.NewMRRCents = money.Cents(core.CHInt64(r["new_mrr"]))
		m.Revenue.ChurnedMRRCents = money.Cents(core.CHInt64(r["churned_mrr"]))
		m.Revenue.NetNewMRRCents = m.Revenue.NewMRRCents - m.Revenue.ChurnedMRRCents
		m.Subs.New = int(core.CHInt64(r["new_count"]))
		m.Subs.Canceled = int(core.CHInt64(r["canceled_count"]))
	}
	// Recent movements feed.
	if rows, err := aiobject.DatastoreQuery(ctx, recentSQL(), sinceTS); err == nil {
		m.Subs.Recent = recentFromRows(rows)
	}
	// Metered usage headline (window).
	if rows, err := aiobject.DatastoreQuery(ctx, usageSQL(), sinceTS); err == nil {
		r := core.CHFirstRow(rows)
		m.Usage.Requests = core.CHInt64(r["requests"])
		m.Usage.WindowUsageCents = money.Cents(core.CHInt64(r["usage_cents"]))
		m.Usage.Instrumented = m.Usage.Requests > 0
	}
	// Fleet org count (any billing activity).
	if rows, err := aiobject.DatastoreQuery(ctx, orgCountSQL()); err == nil {
		m.Orgs = int(core.CHInt64(core.CHFirstRow(rows)["orgs"]))
	}
	// Top customers by MRR + windowed usage (two reads merged, no fan-out).
	m.Customers = topCustomers(ctx, sinceTS, limit)

	m.Gaps = gapsFor(m)
	return core.OK(c, MetricsData{
		SaaSMetrics: normalize(m),
		GeneratedAt: now,
		Sources:     []core.SourceStatus{core.SrcOf("billing-warehouse", nil, m.Orgs, now)},
	})
}

// ── active-subscription state subquery (latest-event-wins, non-canceled) ─────

// activeSubs is the fleet's current subscription state: one row per subscription,
// its LATEST lifecycle values (argMax by timestamp), keeping only non-canceled
// subs (HAVING on the latest event). Static SQL over a closed event-name set — no
// user input interpolated. Reused by every run-rate panel so the definition of
// "active" lives in ONE place.
func activeSubs() string {
	return "(SELECT " +
		"argMax(organization_id, timestamp) AS org, " +
		"argMax(JSONExtractString(properties, 'plan'), timestamp) AS plan, " +
		"argMax(JSONExtractString(properties, 'plan_name'), timestamp) AS plan_name, " +
		"argMax(JSONExtractString(properties, 'category'), timestamp) AS category, " +
		"argMax(JSONExtractString(properties, 'status'), timestamp) AS status, " +
		"argMax(JSONExtractInt(properties, 'mrr_cents'), timestamp) AS mrr_cents, " +
		"argMax(JSONExtractInt(properties, 'seats'), timestamp) AS seats, " +
		"min(timestamp) AS first_ts " +
		"FROM " + core.BillingEventsTable + " " +
		"WHERE event IN (" + core.SQLInList(core.SubscriptionEvents) + ") " +
		"AND JSONExtractString(properties, 'subscription_id') != '' " +
		"GROUP BY JSONExtractString(properties, 'subscription_id') " +
		"HAVING argMax(event, timestamp) != '" + core.EvSubscriptionCanceled + "')"
}

// ── pure SQL builders (static SQL + at most one positional time bound) ────────

// headlineSQL: run-rate MRR (paying, non-trial), active-sub count, paying-customer
// count, and trial count — one pass over the active-subs state.
func headlineSQL() string {
	return "SELECT sumIf(mrr_cents, status != 'trialing') AS mrr, " +
		"count() AS active_subs, " +
		"uniqExactIf(org, status != 'trialing' AND mrr_cents > 0) AS paying, " +
		"countIf(status = 'trialing') AS trials FROM " + activeSubs()
}

func byCategorySQL() string {
	return "SELECT category, sumIf(mrr_cents, status != 'trialing') AS mrr, count() AS subs " +
		"FROM " + activeSubs() + " GROUP BY category ORDER BY mrr DESC"
}

func byPlanSQL() string {
	return "SELECT plan, any(plan_name) AS name, any(category) AS category, " +
		"countIf(status = 'active') AS active, countIf(status = 'trialing') AS trialing, " +
		"sum(seats) AS seats, sumIf(mrr_cents, status != 'trialing') AS mrr " +
		"FROM " + activeSubs() + " GROUP BY plan ORDER BY mrr DESC"
}

// movementSQL: windowed new vs churned MRR + counts (one positional since bound).
func movementSQL() string {
	return "SELECT " +
		"sumIf(JSONExtractInt(properties, 'mrr_cents'), event = '" + core.EvSubscriptionCreated + "') AS new_mrr, " +
		"countIf(event = '" + core.EvSubscriptionCreated + "') AS new_count, " +
		"sumIf(JSONExtractInt(properties, 'mrr_cents'), event = '" + core.EvSubscriptionCanceled + "') AS churned_mrr, " +
		"countIf(event = '" + core.EvSubscriptionCanceled + "') AS canceled_count " +
		"FROM " + core.BillingEventsTable + " " +
		"WHERE event IN ('" + core.EvSubscriptionCreated + "','" + core.EvSubscriptionCanceled + "') AND timestamp >= ?"
}

func recentSQL() string {
	return "SELECT timestamp AS at, organization_id AS org, event AS type, " +
		"JSONExtractString(properties, 'plan_name') AS plan, " +
		"JSONExtractString(properties, 'category') AS category, " +
		"JSONExtractInt(properties, 'mrr_cents') AS mrr_delta " +
		"FROM " + core.BillingEventsTable + " " +
		"WHERE event IN ('" + core.EvSubscriptionCreated + "','" + core.EvSubscriptionCanceled + "') AND timestamp >= ? " +
		"ORDER BY at DESC LIMIT " + strconv.Itoa(recentLimit)
}

func usageSQL() string {
	return "SELECT count() AS requests, sum(JSONExtractInt(properties, 'amount_cents')) AS usage_cents " +
		"FROM " + core.BillingEventsTable + " WHERE event = '" + core.EvAPIUsageDebit + "' AND timestamp >= ?"
}

func orgCountSQL() string {
	return "SELECT uniqExact(organization_id) AS orgs FROM " + core.BillingEventsTable +
		" WHERE event IN (" + core.SQLInList(allBillingEvents()) + ")"
}

func perOrgSubsSQL() string {
	return "SELECT org, sumIf(mrr_cents, status != 'trialing') AS mrr, sum(seats) AS seats, " +
		"argMax(plan_name, mrr_cents) AS plan, argMax(category, mrr_cents) AS category, " +
		"argMax(status, mrr_cents) AS status, min(first_ts) AS since " +
		"FROM " + activeSubs() + " GROUP BY org"
}

func perOrgUsageSQL() string {
	return "SELECT organization_id AS org, sum(JSONExtractInt(properties, 'amount_cents')) AS usage_cents " +
		"FROM " + core.BillingEventsTable + " WHERE event = '" + core.EvAPIUsageDebit + "' AND timestamp >= ? GROUP BY org"
}

// allBillingEvents is the union of every customer-activity event the fleet counts
// an org as "active" on (subscription + invoice + usage).
func allBillingEvents() []string {
	out := append([]string{}, core.SubscriptionEvents...)
	out = append(out, core.InvoiceEvents...)
	return append(out, core.EvAPIUsageDebit)
}

// ── pure row parsers ─────────────────────────────────────────────────────────

func fillHeadline(r *SaaSRevenue, row map[string]any) {
	r.MRRCents = money.Cents(core.CHInt64(row["mrr"]))
	r.ARRCents = r.MRRCents * 12
	r.ActiveSubscriptions = int(core.CHInt64(row["active_subs"]))
	r.PayingCustomers = int(core.CHInt64(row["paying"]))
	r.Trials = int(core.CHInt64(row["trials"]))
}

func byCategoryFromRows(rows []map[string]any) []SaaSCategory {
	out := make([]SaaSCategory, 0, len(rows))
	for _, r := range rows {
		out = append(out, SaaSCategory{
			Category:      core.CHStr(r["category"]),
			MRRCents:      money.Cents(core.CHInt64(r["mrr"])),
			Subscriptions: int(core.CHInt64(r["subs"])),
		})
	}
	return out
}

func byPlanFromRows(rows []map[string]any) []SaaSPlan {
	out := make([]SaaSPlan, 0, len(rows))
	for _, r := range rows {
		out = append(out, SaaSPlan{
			Plan:     core.CHStr(r["plan"]),
			Name:     core.CHStr(r["name"]),
			Category: core.CHStr(r["category"]),
			Active:   int(core.CHInt64(r["active"])),
			Trialing: int(core.CHInt64(r["trialing"])),
			Seats:    int(core.CHInt64(r["seats"])),
			MRRCents: money.Cents(core.CHInt64(r["mrr"])),
		})
	}
	return out
}

func recentFromRows(rows []map[string]any) []SaaSEvent {
	out := make([]SaaSEvent, 0, len(rows))
	for _, r := range rows {
		typ := "created"
		delta := money.Cents(core.CHInt64(r["mrr_delta"]))
		if core.CHStr(r["type"]) == core.EvSubscriptionCanceled {
			typ = "canceled"
			delta = -delta // churn reduces run-rate MRR
		}
		out = append(out, SaaSEvent{
			At:            core.CHTime(r["at"]),
			Org:           core.CHStr(r["org"]),
			Type:          typ,
			Plan:          core.CHStr(r["plan"]),
			Category:      core.CHStr(r["category"]),
			MRRDeltaCents: delta,
		})
	}
	return out
}

// topCustomers folds per-org subscription state + per-org windowed usage into the
// top-N customers by MRR (then usage). Two reads merged in Go by org — a union, so
// a pay-as-you-go org with usage but no subscription still appears.
func topCustomers(ctx context.Context, sinceTS string, limit int) []SaaSCustomer {
	byOrg := map[string]*SaaSCustomer{}
	if rows, err := aiobject.DatastoreQuery(ctx, perOrgSubsSQL()); err == nil {
		for _, r := range rows {
			org := core.CHStr(r["org"])
			if org == "" {
				continue
			}
			byOrg[org] = &SaaSCustomer{
				Org:      org,
				Plan:     core.CHStr(r["plan"]),
				Category: core.CHStr(r["category"]),
				Status:   core.CHStr(r["status"]),
				MRRCents: money.Cents(core.CHInt64(r["mrr"])),
				Seats:    int(core.CHInt64(r["seats"])),
				Since:    core.CHTime(r["since"]),
			}
		}
	}
	if rows, err := aiobject.DatastoreQuery(ctx, perOrgUsageSQL(), sinceTS); err == nil {
		for _, r := range rows {
			org := core.CHStr(r["org"])
			if org == "" {
				continue
			}
			usage := money.Cents(core.CHInt64(r["usage_cents"]))
			if cust, ok := byOrg[org]; ok {
				cust.UsageCents = usage
				continue
			}
			byOrg[org] = &SaaSCustomer{Org: org, Plan: "pay-as-you-go", Status: "active", UsageCents: usage}
		}
	}
	out := make([]SaaSCustomer, 0, len(byOrg))
	for _, c := range byOrg {
		out = append(out, *c)
	}
	sortCustomers(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// ── small pure helpers ───────────────────────────────────────────────────────

// sortCustomers ranks by MRR desc, ties broken by windowed usage desc.
func sortCustomers(cs []SaaSCustomer) {
	sort.SliceStable(cs, func(i, j int) bool { return lessCustomer(cs[i], cs[j]) })
}

func lessCustomer(a, b SaaSCustomer) bool {
	if a.MRRCents != b.MRRCents {
		return a.MRRCents > b.MRRCents
	}
	return a.UsageCents > b.UsageCents
}

// gapsFor lists honest not-yet-observed signals so the console can badge a partial
// snapshot without fabricating data.
func gapsFor(m SaaSMetrics) []string {
	gaps := []string{}
	if !m.Usage.Instrumented {
		gaps = append(gaps, "api-usage debits not yet observed")
	}
	if m.Revenue.ActiveSubscriptions == 0 {
		gaps = append(gaps, "no active subscriptions observed")
	}
	return gaps
}

// empty is the honest not-connected snapshot: real zeros + empty slices (never
// null, never fabricated) plus the not-ok source.
func empty(now, window string, src core.SourceStatus) MetricsData {
	return MetricsData{
		SaaSMetrics: normalize(SaaSMetrics{AsOf: now, Currency: "usd", Window: window}),
		GeneratedAt: now,
		Sources:     []core.SourceStatus{src},
	}
}

// normalize replaces nil slices with empty ones so the JSON is honest arrays (`[]`,
// not null) and the console never has to guard a missing collection.
func normalize(m SaaSMetrics) SaaSMetrics {
	if m.Revenue.ByCategory == nil {
		m.Revenue.ByCategory = []SaaSCategory{}
	}
	if m.Subs.ByPlan == nil {
		m.Subs.ByPlan = []SaaSPlan{}
	}
	if m.Subs.Recent == nil {
		m.Subs.Recent = []SaaSEvent{}
	}
	if m.Customers == nil {
		m.Customers = []SaaSCustomer{}
	}
	if m.Gaps == nil {
		m.Gaps = []string{}
	}
	return m
}

// normalizeWindow clamps ?window to the supported set (default 30d) — mirrors the
// warehouse window grammar (core.WarehouseSince).
func normalizeWindow(v string) string {
	switch strings.TrimSpace(v) {
	case "24h":
		return "24h"
	case "7d":
		return "7d"
	default:
		return "30d"
	}
}

// parseLimit clamps the top-N cap to [1,200], defaulting to defaultLimit.
func parseLimit(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return defaultLimit
	}
	if n > 200 {
		return 200
	}
	return n
}
