// Package subscriptions is the fleet SUBSCRIPTION view (/v1/admin/subscriptions) —
// every tenant's plan subscription: customer/org, plan, status, monthly-normalized
// MRR, and the current-period start/renews. SuperAdmin only (core.Guard).
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
	"sort"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/datastore"
	"github.com/zap-proto/zip"
)

// defaultLimit caps the fleet subscription list when the caller sends none.
const defaultLimit = 500

// SubscriptionRow is one row of GET /v1/admin/subscriptions — a tenant's subscription at
// a glance, tagged with its owning org. MRR is USD cents; timestamps are RFC3339 strings.
type SubscriptionRow struct {
	ID       string `json:"id"`
	Org      string `json:"org"`
	Display  string `json:"display"`
	User     string `json:"user"`
	Plan     string `json:"plan"`
	Status   string `json:"status"`
	MRRCents int64  `json:"mrrCents"`
	Started  string `json:"started"`
	Renews   string `json:"renews"`
}

// Subscriptions answers GET /v1/admin/subscriptions.
//
//	GET /v1/admin/subscriptions?org=&status=&limit=
func Subscriptions(s *cloud.Service[core.State], c *zip.Ctx) error {
	ctx := c.Context()
	status := strings.ToLower(strings.TrimSpace(c.Query("status")))
	wantOrg := strings.TrimSpace(c.Query("org"))
	limit := parseLimit(c.Query("limit"))

	// Honest-empty when the warehouse is not connected or the collector's events
	// table is not provisioned yet (the emitter is still being wired).
	if !core.BillingEventsReady(ctx) {
		return core.OKList(c, []SubscriptionRow{}, 0)
	}

	rows, err := datastore.Query(ctx, subscriptionsSQL())
	if err != nil {
		return core.Fail(c, "subscriptions query: "+err.Error())
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
	return core.OKList(c, out, total)
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
