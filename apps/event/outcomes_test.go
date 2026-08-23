package event

import (
	"strings"
	"testing"
	"time"
)

// TestOutcomesSQL_TenantIsolation is the isolation-invariant test for the experiments
// measurement client: the org is a BOUND argument (never interpolated into SQL) and
// every event name is bound too, so a hostile org slug or event name can never escape
// into the query — the same boundary every analytics builder holds.
func TestOutcomesSQL_TenantIsolation(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	org := "acme'; DROP TABLE event.event;--"
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
	// org is bound, and it LEADS the scope.
	if !strings.Contains(sql, "org = ?") {
		t.Fatalf("query must bind org positionally: %s", sql)
	}
	// args order: [exposure, metric, org, signal, start, end, exposure, metric].
	if len(args) != 8 {
		t.Fatalf("want 8 bound args, got %d: %v", len(args), args)
	}
	if args[0] != exposure || args[1] != metric {
		t.Fatalf("SELECT maxIf binds must lead: %v", args[:2])
	}
	if args[2] != org {
		t.Fatalf("org must be the FIRST scope bind, got %v", args[2])
	}
	if args[3] != string(signalAct) {
		t.Fatalf("signal must be the second scope bind, got %v", args[3])
	}
	if args[6] != exposure || args[7] != metric {
		t.Fatalf("event-set IN binds must trail: %v", args[6:])
	}
}

// TestOutcomesSQL_NoExposureEvent covers the metric-only shape (exposure "" -> every
// subject exposed): one bound event + the four eventsWhere binds, org still leading.
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
	if len(args) != 5 || args[0] != "signup" || args[1] != "acme" || args[2] != string(signalAct) {
		t.Fatalf("args must be [metric, org, signal, start, end], got %v", args)
	}
}
