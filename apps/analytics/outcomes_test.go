package analytics

import (
	"strings"
	"testing"
	"time"
)

// TestOutcomesSQL_TenantIsolation is the isolation-invariant test for the experiments
// measurement seam: the org is a BOUND argument (never interpolated into SQL) and
// every event name is bound too, so a hostile org slug or event name can never escape
// into the query — the same boundary every analytics builder holds.
func TestOutcomesSQL_TenantIsolation(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	org := "acme'; DROP TABLE hanzo.events;--"
	exposure := "$feature_flag_called"
	metric := "order_completed"

	sql, args := outcomesSQL(org, exposure, metric, start, end)

	// The hostile org must NOT appear in the SQL text — only as a bound arg.
	if strings.Contains(sql, org) {
		t.Fatalf("org slug interpolated into SQL (injection): %s", sql)
	}
	if strings.Contains(sql, "DROP TABLE") {
		t.Fatalf("SQL carries injected text: %s", sql)
	}
	// tenant_id is bound; the org is the trailing eventsWhere arg.
	if !strings.Contains(sql, "tenant_id = ?") {
		t.Fatalf("query must bind tenant_id positionally: %s", sql)
	}
	// args order: [exposure, metric, start, end, org, exposure, metric].
	if len(args) != 7 {
		t.Fatalf("want 7 bound args, got %d: %v", len(args), args)
	}
	if args[0] != exposure || args[1] != metric {
		t.Fatalf("SELECT maxIf binds must lead: %v", args[:2])
	}
	if args[4] != org {
		t.Fatalf("org must be the eventsWhere trailing bound arg, got %v", args[4])
	}
	if args[5] != exposure || args[6] != metric {
		t.Fatalf("event-set IN binds must trail: %v", args[5:])
	}
}

// TestOutcomesSQL_NoExposureEvent covers the metric-only shape (exposure "" -> every
// subject exposed): one bound event + the three eventsWhere binds, org still bound.
func TestOutcomesSQL_NoExposureEvent(t *testing.T) {
	start := time.Now().Add(-24 * time.Hour)
	end := time.Now()
	sql, args := outcomesSQL("acme", "", "signup", start, end)
	if strings.Contains(sql, " IN (") {
		t.Fatalf("metric-only query must not build an event-set IN: %s", sql)
	}
	if !strings.Contains(sql, "1 AS exposed") {
		t.Fatalf("metric-only query marks every subject exposed: %s", sql)
	}
	if len(args) != 4 || args[0] != "signup" || args[3] != "acme" {
		t.Fatalf("args must be [metric, start, end, org], got %v", args)
	}
}
