// Package metrics is the fleet SaaS-operations god-view (/v1/admin/metrics) — the
// operator's business dashboard: MRR/ARR, net-new vs churned MRR, the plan/category
// mix, the top customers, and the recent subscription movements. SuperAdmin only
// (core.Admit).
//
// It reads the ONE shared warehouse (commerce.events) — the table the commerce
// analytics collector lands every subscription/invoice/usage-lifecycle event in —
// over the SAME client (datastore.Query) the o11y/compute lenses use, with
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

	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/apps/datastore"
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
	// AsOf is when the read ran, RFC3339 in UTC. The run-rate panels are as-of this
	// instant; the movement panels cover Window ending here.
	AsOf string `json:"asOf"`
	// Currency is the ISO code every *Cents field on this board is denominated in. It is
	// "usd" always — the fleet bills one currency, and the field exists so a consumer
	// never has to assume it.
	Currency string `json:"currency"`
	// Window is the movement window: "24h", "7d" or "30d", clamped to that set. It bounds
	// new/churned MRR, the recent feed and every usage figure. It does NOT bound the
	// run-rate panels, which are as-of AsOf whatever the window says.
	Window string `json:"window"`
	// Revenue is the recurring-revenue headline: run-rate MRR/ARR plus the window's
	// movement.
	Revenue SaaSRevenue `json:"revenue"`
	// Subs is the subscription-operations panel: the per-plan mix and what moved.
	Subs SaaSSubs `json:"subscriptions"`
	// Usage is metered pay-as-you-go spend over the window. It is separate money from
	// MRR and is never folded into it.
	Usage SaaSUsage `json:"usage"`
	// Customers is the top orgs by MRR, then by windowed usage, capped by the request's
	// limit. A union of subscribers and metered spenders, so a pay-as-you-go org with no
	// subscription still appears. Never null.
	Customers []SaaSCustomer `json:"customers"`
	// Orgs is how many distinct organizations have EVER produced a billing event —
	// subscription, invoice or usage. Not windowed, and not a count of paying customers.
	Orgs int `json:"orgs"`
	// Gaps names the signals this snapshot has not observed, one sentence each, so a
	// console can badge a partial board instead of presenting a zero as a fact. Empty
	// means every panel had data. Never null.
	Gaps []string `json:"gaps"`
}

// SaaSRevenue is the recurring-revenue headline (run-rate MRR/ARR + windowed movement).
type SaaSRevenue struct {
	// MRRCents is run-rate monthly recurring revenue, in USD cents, summed over the
	// subscriptions whose LATEST lifecycle event left them `active`. Trialing, past_due
	// and unpaid subscriptions contribute nothing — the predicate is exactly commerce's
	// CountsTowardMRR, spelled in SQL because this board reads the warehouse rather than
	// calling the Go function.
	MRRCents money.Cents `json:"mrrCents"`
	// ARRCents is MRRCents times twelve. A run-rate restatement of today's MRR, not a
	// forecast: nothing in it anticipates growth, churn or an annual discount.
	ARRCents money.Cents `json:"arrCents"`
	// ActiveSubscriptions counts every non-canceled subscription — trialing and past_due
	// included. It is deliberately a wider set than the one MRRCents sums, so this can be
	// non-zero while MRR is zero.
	ActiveSubscriptions int `json:"activeSubscriptions"`
	// PayingCustomers counts distinct ORGS with at least one active subscription carrying
	// MRR above zero. Orgs, not subscriptions: an org on three plans counts once.
	PayingCustomers int `json:"payingCustomers"`
	// Trials counts subscriptions currently in a trial. Revenue that has not started, so
	// it is reported beside MRR and never inside it.
	Trials int `json:"trials"`
	// NewMRRCents is the MRR carried by subscriptions CREATED inside the window. Gross,
	// and positive.
	NewMRRCents money.Cents `json:"newMrrCents"`
	// ChurnedMRRCents is the MRR carried by subscriptions CANCELED inside the window,
	// reported as a POSITIVE magnitude — the sign convention lives in NetNewMRRCents,
	// which subtracts it, not here.
	ChurnedMRRCents money.Cents `json:"churnedMrrCents"`
	// NetNewMRRCents is NewMRRCents minus ChurnedMRRCents. Negative means the fleet lost
	// more recurring revenue than it won over the window. It counts only creations and
	// cancellations — an upgrade or downgrade in place moves MRRCents without appearing
	// here.
	NetNewMRRCents money.Cents `json:"netNewMrrCents"`
	// ByCategory is the plan mix: run-rate MRR per plan category, largest first. It sums
	// to MRRCents. Never null.
	ByCategory []SaaSCategory `json:"byCategory"`
}

// SaaSCategory is one plan-category bucket of run-rate MRR (the plan mix).
type SaaSCategory struct {
	// Category is the plan's category as the subscription event declared it. Empty is a
	// real bucket — subscriptions whose plan carries no category land there.
	Category string `json:"category"`
	// MRRCents is this category's run-rate MRR, active subscriptions only.
	MRRCents money.Cents `json:"mrrCents"`
	// Subscriptions counts every non-canceled subscription in the category, trialing
	// included. So a category of nothing but trials shows subscriptions above zero and
	// mrrCents at zero, which is the honest reading.
	Subscriptions int `json:"subscriptions"`
}

// SaaSSubs is the subscription-operations panel (per-plan mix, trials, new/canceled,
// recent movements).
type SaaSSubs struct {
	// ByPlan is every plan the fleet has a live subscription on, richest first by MRR.
	// Never null.
	ByPlan []SaaSPlan `json:"byPlan"`
	// TrialsActive is how many subscriptions are trialing right now. The same number as
	// revenue.trials, restated on the panel an operator works trials from.
	TrialsActive int `json:"trialsActive"`
	// New is how many subscriptions were created inside the window.
	New int `json:"new"`
	// Canceled is how many were canceled inside the window. Not a rate: divide by New, or
	// by activeSubscriptions, only if you mean to.
	Canceled int `json:"canceled"`
	// Recent is the movement feed, newest first, capped at twenty and bounded by the same
	// window. It is a sample of what New and Canceled counted, not the whole of it.
	// Never null.
	Recent []SaaSEvent `json:"recent"`
}

// SaaSPlan is one plan's active/trialing counts, seats, and MRR contribution.
type SaaSPlan struct {
	// Plan is the plan's stable id — the key subscriptions are grouped by.
	Plan string `json:"plan"`
	// Name is the plan's human label, as the warehouse last saw it. A plan renamed
	// mid-life shows one of its names, not both.
	Name string `json:"name"`
	// Category is the plan's category, the same bucket revenue.byCategory sums into.
	Category string `json:"category"`
	// Active counts this plan's subscriptions in the `active` state — the ones MRRCents
	// is summed over.
	Active int `json:"active"`
	// Trialing counts this plan's subscriptions still in a trial. Not yet revenue.
	Trialing int `json:"trialing"`
	// Seats is licensed seats across ALL of this plan's non-canceled subscriptions,
	// trialing included — a wider set than Active, so seats can be non-zero on a plan
	// carrying no MRR at all.
	Seats int `json:"seats"`
	// MRRCents is this plan's contribution to run-rate MRR, active subscriptions only.
	MRRCents money.Cents `json:"mrrCents"`
}

// SaaSEvent is one recent subscription movement ("created" or "canceled").
type SaaSEvent struct {
	// At is when the movement happened, RFC3339. The feed is ordered by it, newest first.
	At string `json:"at"`
	// Org is the organization whose subscription moved.
	Org string `json:"org"`
	// Type is `created` or `canceled` — the warehouse's event name normalised to the two
	// movements this feed carries.
	Type string `json:"type"`
	// Plan is the plan's human label at the moment of the movement.
	Plan string `json:"plan"`
	// Category is the plan's category at the moment of the movement — the same bucket
	// revenue.byCategory sums into.
	Category string `json:"category"`
	// MRRDeltaCents is SIGNED: positive for a creation, negative for a cancellation. It
	// is the run-rate this one movement added or took away, so summing the feed over the
	// window gives revenue.netNewMrrCents.
	MRRDeltaCents money.Cents `json:"mrrDeltaCents"`
}

// SaaSUsage is the metered / pay-as-you-go revenue headline for the window.
type SaaSUsage struct {
	// Instrumented reports that at least one metered debit was observed in the window.
	// False means NOT MEASURED — the two fields below are then meaningless rather than
	// zero, and gaps says so. It is the difference between "nobody used the API" and
	// "nothing is emitting usage events yet".
	Instrumented bool `json:"instrumented"`
	// WindowUsageCents is metered API spend over the window, in USD cents. Consumption
	// revenue, entirely separate from MRR — adding the two double-counts nothing but
	// invents a figure the business does not have.
	WindowUsageCents money.Cents `json:"windowUsageCents"`
	// Requests is how many billed API calls the window recorded — one per debit event.
	Requests int64 `json:"requests"`
}

// SaaSCustomer is one top customer by MRR + windowed usage.
type SaaSCustomer struct {
	// Org is the organization id, which is the row's identity.
	Org string `json:"org"`
	// Plan is the label of the org's LARGEST subscription by MRR, so an org on several
	// plans is named by the one it pays most for. An org with usage but no subscription
	// at all reads "pay-as-you-go".
	Plan string `json:"plan"`
	// Category is that plan's category. Empty for a pay-as-you-go org.
	Category string `json:"category"`
	// Status is that subscription's state — `active`, `trialing`, `past_due`. A
	// pay-as-you-go org reads "active", since it is spending.
	Status string `json:"status"`
	// MRRCents is the org's run-rate MRR, summed over its active subscriptions. Zero for
	// a pay-as-you-go org, which is a true zero and not a gap.
	MRRCents money.Cents `json:"mrrCents"`
	// UsageCents is the org's metered spend over the window. It is the tiebreaker in the
	// ranking, and the only revenue a pay-as-you-go org has.
	UsageCents money.Cents `json:"usageCents"`
	// Seats is licensed seats across all the org's non-canceled subscriptions.
	Seats int `json:"seats"`
	// Since is when the org's OLDEST live subscription began, RFC3339 — how long it has
	// been a customer. Omitted for an org that has never subscribed.
	Since string `json:"since,omitempty"`
}

// MetricsData is the GET /v1/admin/metrics payload: the SaaS snapshot, flat, plus the
// admin read time and the upstream freshness strip every god-view carries.
type MetricsData struct {
	SaaSMetrics
	// GeneratedAt is when the admin plane served this read, RFC3339. Same instant as
	// asOf: this board computes on demand and caches nothing, so the two cannot diverge.
	GeneratedAt string `json:"generatedAt"`
	// Sources is the upstream freshness strip. Exactly one row, "billing-warehouse", and
	// its ok=false is what distinguishes a fleet with no revenue from a deployment where
	// the warehouse is not connected — both otherwise answer zeros.
	Sources []core.SourceStatus `json:"sources"`
}

// Metrics answers GET /v1/admin/metrics by aggregating commerce.events directly
// (fleet-wide, no per-org fan-out). SuperAdmin only.
//
//	GET /v1/admin/metrics?window=30d&limit=20
func Metrics(ctx context.Context, in *MetricsIn) (*MetricsOut, error) {
	if _, err := core.Admit(ctx); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	window := core.WarehouseRange(in.Window)
	limit := parseLimit(in.Limit)

	// Honest not-configured snapshot when the warehouse/collector table is absent.
	if !core.BillingEventsReady(ctx) {
		zero := empty(now, window, core.SrcOf("billing-warehouse", errUnconfigured, 0, now))
		return &MetricsOut{Status: core.OK, Data: &zero}, nil
	}

	sinceTS := core.CHTimeLit(core.WarehouseSince(window))
	m := SaaSMetrics{AsOf: now, Currency: "usd", Window: window}

	// Revenue headline + plan-mix (run-rate, latest-event-wins over active subs).
	if rows, err := datastore.Query(ctx, headlineSQL()); err == nil {
		fillHeadline(&m.Revenue, core.CHFirstRow(rows))
	}
	if rows, err := datastore.Query(ctx, byCategorySQL()); err == nil {
		m.Revenue.ByCategory = byCategoryFromRows(rows)
	}
	if rows, err := datastore.Query(ctx, byPlanSQL()); err == nil {
		m.Subs.ByPlan = byPlanFromRows(rows)
	}
	m.Subs.TrialsActive = m.Revenue.Trials

	// Windowed movement: new vs churned MRR + counts.
	if rows, err := datastore.Query(ctx, movementSQL(), sinceTS); err == nil {
		r := core.CHFirstRow(rows)
		m.Revenue.NewMRRCents = money.Cents(core.CHInt64(r["new_mrr"]))
		m.Revenue.ChurnedMRRCents = money.Cents(core.CHInt64(r["churned_mrr"]))
		m.Revenue.NetNewMRRCents = m.Revenue.NewMRRCents - m.Revenue.ChurnedMRRCents
		m.Subs.New = int(core.CHInt64(r["new_count"]))
		m.Subs.Canceled = int(core.CHInt64(r["canceled_count"]))
	}
	// Recent movements feed.
	if rows, err := datastore.Query(ctx, recentSQL(), sinceTS); err == nil {
		m.Subs.Recent = recentFromRows(rows)
	}
	// Metered usage headline (window).
	if rows, err := datastore.Query(ctx, usageSQL(), sinceTS); err == nil {
		r := core.CHFirstRow(rows)
		m.Usage.Requests = core.CHInt64(r["requests"])
		m.Usage.WindowUsageCents = money.Cents(core.CHInt64(r["usage_cents"]))
		m.Usage.Instrumented = m.Usage.Requests > 0
	}
	// Fleet org count (any billing activity).
	if rows, err := datastore.Query(ctx, orgCountSQL()); err == nil {
		m.Orgs = int(core.CHInt64(core.CHFirstRow(rows)["orgs"]))
	}
	// Top customers by MRR + windowed usage (two reads merged, no fan-out).
	m.Customers = topCustomers(ctx, sinceTS, limit)

	m.Gaps = gapsFor(m)
	return &MetricsOut{Status: core.OK, Data: &MetricsData{
		SaaSMetrics: normalize(m),
		GeneratedAt: now,
		Sources:     []core.SourceStatus{core.SrcOf("billing-warehouse", nil, m.Orgs, now)},
	}}, nil
}

// MetricsIn is the GET /v1/admin/metrics query.
type MetricsIn struct {
	// Window is the movement window the new/churned MRR and the recent feed are
	// measured over. Anything unrecognised falls back to the board default.
	Window string `json:"window"`
	// Limit caps the top-customers table.
	Limit string `json:"limit"`
}

// MetricsOut is the GET /v1/admin/metrics envelope.
type MetricsOut struct {
	// Status is "ok" or "error". A warehouse that is not connected still answers ok, with
	// real zeros and a not-ok source row — an honest empty board rather than a failure.
	Status string `json:"status"`
	// Msg is the failure, and is empty on success.
	Msg string `json:"msg"`
	// Data is the board. Never null on an ok answer.
	Data *MetricsData `json:"data"`
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
//
// The revenue predicate is `status = 'active'`, which is commerce's
// subscription.Status.CountsTowardMRR spelled in SQL — this board reads the
// warehouse, so it cannot call the Go function, and the two must be kept in
// step by hand. It used to say `status != 'trialing'`, which also counted
// past_due and unpaid as run-rate revenue, so this board and the money board
// reported different MRR for the same account in both directions at once.
func headlineSQL() string {
	return "SELECT sumIf(mrr_cents, status = 'active') AS mrr, " +
		"count() AS active_subs, " +
		"uniqExactIf(org, status = 'active' AND mrr_cents > 0) AS paying, " +
		"countIf(status = 'trialing') AS trials FROM " + activeSubs()
}

func byCategorySQL() string {
	return "SELECT category, sumIf(mrr_cents, status = 'active') AS mrr, count() AS subs " +
		"FROM " + activeSubs() + " GROUP BY category ORDER BY mrr DESC"
}

func byPlanSQL() string {
	return "SELECT plan, any(plan_name) AS name, any(category) AS category, " +
		"countIf(status = 'active') AS active, countIf(status = 'trialing') AS trialing, " +
		"sum(seats) AS seats, sumIf(mrr_cents, status = 'active') AS mrr " +
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
	return "SELECT org, sumIf(mrr_cents, status = 'active') AS mrr, sum(seats) AS seats, " +
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
	if rows, err := datastore.Query(ctx, perOrgSubsSQL()); err == nil {
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
	if rows, err := datastore.Query(ctx, perOrgUsageSQL(), sinceTS); err == nil {
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
