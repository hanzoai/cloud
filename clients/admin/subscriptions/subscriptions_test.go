package subscriptions

import (
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/clients/admin/core"
)

// TestSubscriptionRowsFromRows proves the warehouse-row → SubscriptionRow mapping
// (the JSON-shape contract) coerces the datastore driver's native types and folds
// the lifecycle status; display honestly mirrors the org slug (no fan-out).
func TestSubscriptionRowsFromRows(t *testing.T) {
	started := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	rows := []map[string]any{
		{ // active (mrr as driver int64), latest event renewed
			"id": "sub_1", "org": "acme", "user": "hanzo/alice",
			"plan": "Pro", "status": "active", "mrr_cents": int64(4900),
			"last_event": core.EvSubscriptionRenewed, "started": started,
			"renews": "2026-08-01T00:00:00Z",
		},
		{ // canceled wins over a stale "active" snapshot
			"id": "sub_2", "org": "beta", "user": "hanzo/bob",
			"plan": "Team", "status": "active", "mrr_cents": uint64(0),
			"last_event": core.EvSubscriptionCanceled, "started": started,
			"renews": "",
		},
	}
	out := subscriptionRowsFromRows(rows)
	if len(out) != 2 {
		t.Fatalf("got %d rows, want 2", len(out))
	}
	r0 := out[0]
	if r0.ID != "sub_1" || r0.Org != "acme" || r0.Display != "acme" || r0.User != "hanzo/alice" {
		t.Fatalf("row0 identity wrong: %+v", r0)
	}
	if r0.Plan != "Pro" || r0.Status != "active" || r0.MRRCents != 4900 {
		t.Fatalf("row0 plan/status/mrr wrong: %+v", r0)
	}
	if r0.Started != "2026-07-01T12:00:00Z" {
		t.Fatalf("row0 started = %q", r0.Started)
	}
	if r0.Renews != "2026-08-01T00:00:00Z" {
		t.Fatalf("row0 renews = %q", r0.Renews)
	}
	if out[1].Status != "canceled" {
		t.Fatalf("row1 status = %q, want canceled (latest-event folds)", out[1].Status)
	}
}

func TestFoldStatus(t *testing.T) {
	if got := foldStatus(core.EvSubscriptionCanceled, "active"); got != "canceled" {
		t.Fatalf("cancel fold = %q", got)
	}
	if got := foldStatus(core.EvSubscriptionRenewed, "trialing"); got != "trialing" {
		t.Fatalf("snapshot passthrough = %q", got)
	}
	if got := foldStatus(core.EvSubscriptionCreated, ""); got != "active" {
		t.Fatalf("empty-snapshot default = %q", got)
	}
}

// TestSubscriptionsSQLInjectionSafe asserts the query is fully static over the
// closed event-name set — the warehouse table, no user-derived interpolation.
func TestSubscriptionsSQLInjectionSafe(t *testing.T) {
	sql := subscriptionsSQL()
	if !strings.Contains(sql, core.BillingEventsTable) {
		t.Fatalf("query must read %s: %q", core.BillingEventsTable, sql)
	}
	for _, ev := range core.SubscriptionEvents {
		if !strings.Contains(sql, "'"+ev+"'") {
			t.Fatalf("query missing event %q", ev)
		}
	}
	if strings.Contains(sql, "?") {
		t.Fatalf("subscriptions state query takes no positional args: %q", sql)
	}
}

func TestParseLimitBounds(t *testing.T) {
	if parseLimit("") != defaultLimit || parseLimit("0") != defaultLimit || parseLimit("x") != defaultLimit {
		t.Fatal("bad/empty limit must default")
	}
	if parseLimit("10") != 10 {
		t.Fatal("valid limit must pass through")
	}
	if parseLimit("999999") != 5000 {
		t.Fatal("limit must clamp to 5000")
	}
}
