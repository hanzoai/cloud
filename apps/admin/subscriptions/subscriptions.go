// Package subscriptions is the fleet SUBSCRIPTION view (/v1/admin/subscriptions) —
// every tenant's plan subscription: customer/org, plan, status, monthly-normalized
// MRR, and the current-period start/renews. SuperAdmin only (core.Admit).
//
// It reads the ONE shared warehouse (commerce.events) — the table the commerce
// analytics collector lands every subscription-lifecycle event in — over the SAME
// client (datastore.Query) the o11y/compute lenses use, with ZERO per-org
// fan-out: one GROUP BY resolves each subscription's LATEST lifecycle state
// (argMax by timestamp), so the whole fleet is one query, not N per-org commerce
// reads. Honest by construction: no datastore connected or the collector's table
// not provisioned yet → the real empty list, never a fabricated tenant. The MRR is
// the monthly-normalized figure the emitter already computed (cents). Optional
// ?org= scopes to one tenant, ?status= filters the LATEST status, ?limit= caps.
package subscriptions

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/datastore"
)

// defaultLimit caps the fleet subscription list when the caller sends none.
const defaultLimit = 500

// SubscriptionRow is one row of GET /v1/admin/subscriptions — a tenant's subscription at
// a glance, tagged with its owning org. MRR is USD cents; timestamps are RFC3339 strings.
type SubscriptionRow struct {
	// ID is commerce's subscription id — the row's identity and the warehouse GROUP BY
	// key. One row per subscription, so an org on several plans has several rows.
	ID string `json:"id"`
	// Org is the tenant that holds the subscription, and what ?org= matches exactly.
	Org string `json:"org"`
	// Display is the same slug as Org. The warehouse holds no friendly name and this read
	// does no per-org IAM fan-out, so it repeats the slug rather than inventing one.
	Display string `json:"display"`
	// User is the individual the subscription was created by, from the event's
	// distinct_id. Empty for one created by a machine rather than a person.
	User string `json:"user"`
	// Plan is the plan's human label as of the subscription's latest event. A plan
	// renamed mid-life reports the name it carries now.
	Plan string `json:"plan"`
	// Status is the EFFECTIVE lifecycle state: `canceled` when the latest event is a
	// cancel, whatever the last status snapshot said, otherwise that snapshot, defaulting
	// to `active`. Common values are `active`, `trialing`, `past_due`, `canceled`.
	Status string `json:"status"`
	// MRRCents is the subscription's monthly recurring revenue in USD cents — already
	// interval-normalized and multiplied by seats by the emitter, so an annual plan
	// reports a twelfth here and not its yearly price. Reported for EVERY row, including
	// trialing and canceled ones, which are not revenue: filter on Status before summing.
	MRRCents int64 `json:"mrrCents"`
	// Started is the earliest event on this subscription, RFC3339 — when it first
	// appeared in the warehouse. The tiebreaker in the ranking, newest first.
	Started string `json:"started"`
	// Renews is the current period's end, RFC3339 — when it next bills. On a canceled
	// subscription it is the last period's end, so it is a date in the past, not a
	// promise of a charge.
	Renews string `json:"renews"`
}

// Subscriptions answers GET /v1/admin/subscriptions.
//
//	GET /v1/admin/subscriptions?org=&status=&limit=
func Subscriptions(ctx context.Context, in *SubscriptionsIn) (*SubscriptionsOut, error) {
	if _, err := core.Admit(ctx); err != nil {
		return nil, err
	}
	status := strings.ToLower(strings.TrimSpace(in.Status))
	wantOrg := strings.TrimSpace(in.Org)
	limit := parseLimit(in.Limit)

	// Honest-empty when the warehouse is not connected or the collector's events
	// table is not provisioned yet (the emitter is still being wired).
	if !core.BillingEventsReady(ctx) {
		return &SubscriptionsOut{Status: core.OK, Data: []SubscriptionRow{}, Total: core.Total(0)}, nil
	}

	rows, err := datastore.Query(ctx, subscriptionsSQL())
	if err != nil {
		return &SubscriptionsOut{Status: core.Err, Msg: "subscriptions query: " + err.Error()}, nil
	}
	all := subscriptionRowsFromRows(rows)

	// Filter (latest status / org) then sort highest-MRR first, cap to limit.
	out := make([]SubscriptionRow, 0, len(all))
	for _, r := range all {
		if wantOrg != "" && r.Org != wantOrg {
			continue
		}
		if status != "" && strings.ToLower(r.Status) != status {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MRRCents != out[j].MRRCents {
			return out[i].MRRCents > out[j].MRRCents
		}
		return out[i].Started > out[j].Started
	})
	total := len(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return &SubscriptionsOut{Status: core.OK, Data: out, Total: core.Total(total)}, nil
}

// SubscriptionsIn is the GET /v1/admin/subscriptions filter.
type SubscriptionsIn struct {
	// Status filters on the subscription's LATEST lifecycle status (active, trialing,
	// canceled, …), matched case-insensitively.
	Status string `json:"status"`
	// Org filters to one tenant, matched exactly.
	Org string `json:"org"`
	// Limit caps the rows returned. total still reports the full match count.
	Limit string `json:"limit"`
}

// SubscriptionsOut is the GET /v1/admin/subscriptions envelope. total is the count
// BEFORE limit truncates.
type SubscriptionsOut struct {
	// Status is "ok" or "error". A warehouse that is not connected answers ok with an
	// empty list and total 0 — the honest not-yet-wired state, not a fleet with no
	// subscribers.
	Status string `json:"status"`
	// Msg is the query failure, and is empty on success.
	Msg string `json:"msg"`
	// Data is the matching subscriptions, highest MRR first, then newest, capped by
	// limit. Canceled subscriptions are included unless ?status= excludes them.
	Data []SubscriptionRow `json:"data"`
	// Total is how many matched BEFORE limit truncated. Omitted on an error.
	Total *int `json:"total,omitempty"`
}

// subscriptionsSQL resolves each subscription's LATEST lifecycle state from
// commerce.events (argMax by timestamp). Static SQL over a closed event-name set
// (SQLInList of server constants) — no user input is interpolated, so it is
// injection-safe. The emitted properties carry the plan/status/mrr/period fields.
func subscriptionsSQL() string {
	return "SELECT JSONExtractString(properties, 'subscription_id') AS id, " +
		"argMax(organization_id, timestamp) AS org, " +
		"argMax(distinct_id, timestamp) AS user, " +
		"argMax(JSONExtractString(properties, 'plan_name'), timestamp) AS plan, " +
		"argMax(JSONExtractString(properties, 'status'), timestamp) AS status, " +
		"argMax(JSONExtractInt(properties, 'mrr_cents'), timestamp) AS mrr_cents, " +
		"argMax(event, timestamp) AS last_event, " +
		"min(timestamp) AS started, " +
		"argMax(JSONExtractString(properties, 'period_end'), timestamp) AS renews " +
		"FROM " + core.BillingEventsTable + " " +
		"WHERE event IN (" + core.SQLInList(core.SubscriptionEvents) + ") " +
		"AND JSONExtractString(properties, 'subscription_id') != '' " +
		"GROUP BY id"
}

// subscriptionRowsFromRows maps the datastore rows onto []SubscriptionRow (pure).
// Display is the org slug — the warehouse holds no friendly name and admin does
// no per-org IAM fan-out here (honest, not fabricated). The final status folds
// the lifecycle: a subscription whose LATEST event is a cancel reads "canceled"
// regardless of the last-emitted status snapshot.
func subscriptionRowsFromRows(rows []map[string]any) []SubscriptionRow {
	out := make([]SubscriptionRow, 0, len(rows))
	for _, r := range rows {
		org := core.CHStr(r["org"])
		out = append(out, SubscriptionRow{
			ID:       core.CHStr(r["id"]),
			Org:      org,
			Display:  org,
			User:     core.CHStr(r["user"]),
			Plan:     core.CHStr(r["plan"]),
			Status:   foldStatus(core.CHStr(r["last_event"]), core.CHStr(r["status"])),
			MRRCents: core.CHInt64(r["mrr_cents"]),
			Started:  core.CHTime(r["started"]),
			Renews:   core.CHStr(r["renews"]),
		})
	}
	return out
}

// foldStatus resolves the effective status: a subscription whose latest event is
// a cancel is "canceled"; otherwise the last-emitted status snapshot (falling
// back to "active" when the emitter sent none).
func foldStatus(lastEvent, snapshot string) string {
	if lastEvent == core.EvSubscriptionCanceled {
		return "canceled"
	}
	if s := strings.TrimSpace(snapshot); s != "" {
		return s
	}
	return "active"
}

// parseLimit clamps the fleet-list cap to [1,5000], defaulting to defaultLimit.
func parseLimit(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return defaultLimit
	}
	if n > 5000 {
		return 5000
	}
	return n
}
