package admin

import (
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
)

// TestSubsystemSQL_UsesV3Columns is the regression guard for the bug this board was
// built on top of: distributed_o11y_index_v3 is snake_case and spells resource
// attributes with $$. Querying the v2 spellings (durationNano / serviceName) does not
// fail loudly — the caller swallows the error and the board shows honest-looking zeros
// forever. Pin the real column names.
func TestSubsystemSQL_UsesV3Columns(t *testing.T) {
	for _, sql := range []string{subsystemREDSQL(), subsystemLastErrorSQL(), o11yTraceTotalsSQL(), o11yTopServicesSQL()} {
		if strings.Contains(sql, "durationNano") || strings.Contains(sql, "serviceName") {
			t.Errorf("v2 column spelling in a v3 query — it will silently return nothing: %q", sql)
		}
	}
	if !strings.Contains(subsystemREDSQL(), "duration_nano") {
		t.Errorf("RED query must measure duration_nano; got %q", subsystemREDSQL())
	}
	if !strings.Contains(o11yTraceTotalsSQL(), "resource_string_service$$name") {
		t.Errorf("trace totals must count the v3 service column; got %q", o11yTraceTotalsSQL())
	}
}

// TestSubsystemSQL_Shape proves both reads hit the ONE trace table, group by the ONE
// subsystem label the tracing middleware writes, and bind the time bound POSITIONALLY
// (no interpolation anywhere).
func TestSubsystemSQL_Shape(t *testing.T) {
	for name, sql := range map[string]string{"red": subsystemREDSQL(), "lastError": subsystemLastErrorSQL()} {
		if !strings.Contains(sql, "FROM "+o11yTraceTable) {
			t.Errorf("%s must read %s; got %q", name, o11yTraceTable, sql)
		}
		if !strings.Contains(sql, subsystemAttr) {
			t.Errorf("%s must group by %s; got %q", name, subsystemAttr, sql)
		}
		if n := strings.Count(sql, "?"); n != 1 {
			t.Errorf("%s: %d bind params, want 1 (the time bound only); got %q", name, n, sql)
		}
	}
	if !strings.Contains(subsystemLastErrorSQL(), "has_error") {
		t.Errorf("last-error query must select only errored spans; got %q", subsystemLastErrorSQL())
	}
}

// TestSubsystemRows_InventoryIsTheRowSet proves a mounted-but-silent subsystem still
// gets a row (a real zero), and that prefixes serialize as [] not null.
func TestSubsystemRows_InventoryIsTheRowSet(t *testing.T) {
	rows := subsystemRows([]cloud.Subsystem{
		{Name: "kms", Prefixes: []string{"/v1/kms"}, Enabled: true},
		{Name: "ads", Enabled: false},
	})
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2 — the inventory is the row set", len(rows))
	}
	if rows[1].Prefixes == nil {
		t.Error("nil prefixes must project to an empty slice, so JSON carries [] not null")
	}
	if rows[0].Requests != 0 || rows[0].LastErrorAt != "" {
		t.Error("an un-joined row must be honest zeros, never fabricated")
	}
}

// TestApplyRED_JoinsAndDerivesRate proves the warehouse join lands on the right row,
// derives the per-minute rate from the window, and DROPS a name this binary does not
// have (a rename or a foreign build sharing the warehouse).
func TestApplyRED_JoinsAndDerivesRate(t *testing.T) {
	rows := subsystemRows([]cloud.Subsystem{{Name: "kms", Enabled: true}, {Name: "ads", Enabled: true}})
	idx := rowIndex(rows)

	applyRED(rows, idx, []map[string]any{
		{"subsystem": "kms", "requests": int64(120), "errors": int64(6), "error_rate": 5.0, "p50": 1.5, "p95": 9.0, "p99": 20.0},
		{"subsystem": "ghost", "requests": int64(999)}, // not in this binary — must be dropped
	}, 60*time.Minute)

	kms := rows[0]
	if kms.Requests != 120 || kms.Errors != 6 || kms.ErrorRate != 5.0 {
		t.Errorf("kms RED = %+v, want 120 req / 6 err / 5%%", kms)
	}
	if kms.LatencyP95Ms != 9.0 || kms.LatencyP99Ms != 20.0 {
		t.Errorf("kms latency = p95 %v p99 %v, want 9/20", kms.LatencyP95Ms, kms.LatencyP99Ms)
	}
	if kms.RequestsPerMin != 2 { // 120 requests over a 60-minute window
		t.Errorf("kms rate = %v/min, want 2", kms.RequestsPerMin)
	}
	if rows[1].Requests != 0 {
		t.Errorf("ads must stay zero; a foreign subsystem name must never bleed onto another row")
	}
}

// TestApplyLastError_LandsOnTheRightRow proves the last-error columns join by name.
func TestApplyLastError_LandsOnTheRightRow(t *testing.T) {
	rows := subsystemRows([]cloud.Subsystem{{Name: "kms", Enabled: true}, {Name: "ads", Enabled: true}})
	idx := rowIndex(rows)

	applyLastError(rows, idx, []map[string]any{
		{"subsystem": "ads", "at": "2026-07-27T10:00:00Z", "route": "/v1/ads/serve", "status": "500", "message": "upstream timeout"},
	})

	if rows[0].LastErrorRoute != "" {
		t.Error("kms had no error; its last-error columns must stay empty")
	}
	ads := rows[1]
	if ads.LastErrorRoute != "/v1/ads/serve" || ads.LastErrorStatus != "500" || ads.LastErrorMessage != "upstream timeout" {
		t.Errorf("ads last error = %+v, want the /v1/ads/serve 500", ads)
	}
}

// TestSubsystemTotals proves the KPI fold, and specifically that Reporting counts only
// subsystems that are ON and actually served traffic — the gap against Enabled is the
// operator's "mounted but receiving nothing" signal.
func TestSubsystemTotals(t *testing.T) {
	got := subsystemTotals{}.from([]subsystemRow{
		{Name: "kms", Enabled: true, Requests: 100, Errors: 5},
		{Name: "ai", Enabled: true, Requests: 300, Errors: 15},
		{Name: "idle", Enabled: true}, // on, but silent — not Reporting
		{Name: "ads", Enabled: false}, // off
	})

	want := subsystemTotals{Subsystems: 4, Enabled: 3, Disabled: 1, Reporting: 2, Requests: 400, Errors: 20, ErrorRate: 5}
	if got != want {
		t.Errorf("totals = %+v, want %+v", got, want)
	}
}

// TestSubsystemTotals_NoTrafficIsZeroNotNaN guards the division in the error rate.
func TestSubsystemTotals_NoTrafficIsZeroNotNaN(t *testing.T) {
	got := subsystemTotals{}.from([]subsystemRow{{Name: "kms", Enabled: true}})
	if got.ErrorRate != 0 {
		t.Errorf("error rate with no requests = %v, want 0", got.ErrorRate)
	}
}
