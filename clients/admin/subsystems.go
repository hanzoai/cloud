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

package admin

// subsystems — GET /v1/admin/subsystems, the per-SUBSYSTEM lens on the one binary.
//
// /v1/admin/o11y answers "how is the fleet", by org / model / service. This answers
// the question that one cannot: the cloud binary is a SINGLE process mounting ~60
// subsystems, so every request already shares one service name. Splitting them apart
// needs a label, and there is exactly one: cloud.TracingMiddleware stamps
// hanzo.subsystem onto the request span it already emits, resolved through
// cloud.SubsystemOf against the boot-time mount index. Sixty packages stay
// uninstrumented and NO second metrics path exists — this reads the SAME
// o11y_traces table, over the SAME datastore client, as the o11y board next door.
//
// Two halves, deliberately different in kind:
//
//   - The INVENTORY (name, prefixes, enabled) is process-local: cloud.Subsystems() is
//     the composition root's own index, so it is always truthful, needs no warehouse,
//     and answers "is that board empty because it is broken or because it is OFF".
//   - The RED signals (request rate, error rate, latency percentiles, last error) come
//     from the trace warehouse and degrade independently — an absent or erroring table
//     contributes honest zeros and a not-ok core.SourceStatus, never a fabricated rate.
//
// So with no datastore connected this endpoint still renders the full subsystem list
// with real enable/disable state, and says plainly that telemetry is unavailable.
//
// SUPERADMIN ONLY (core.Guard): the inventory describes the whole binary, and the
// signals cross every tenant. Latency is milliseconds; time bounds are POSITIONAL
// parameters, never interpolated.

import (
	"errors"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/hanzoai/cloud/clients/datastore"
	"github.com/zap-proto/zip"
)

// errTracesUnconfigured marks the trace warehouse absent on this deployment. core.SrcOf
// renders it as a not-ok source, so the board says "telemetry unavailable" beside a
// still-truthful inventory instead of implying every subsystem served zero requests.
var errTracesUnconfigured = errors.New("trace warehouse not connected")

// subsystemAttr is the span attribute cloud.TracingMiddleware stamps — the ONE label
// that separates the co-resident subsystems. Spelled once here and reused by every
// query below, so the reader and the writer cannot drift apart on the key.
const subsystemAttr = "attributes_string['hanzo.subsystem']"

// subsystemBoard is the whole per-subsystem board payload.
type subsystemBoard struct {
	Range   string              `json:"range"`
	Start   string              `json:"start"`
	End     string              `json:"end"`
	Totals  subsystemTotals     `json:"totals"`
	Rows    []subsystemRow      `json:"rows"`
	Sources []core.SourceStatus `json:"sources"`
}

// subsystemTotals is the KPI band above the table.
type subsystemTotals struct {
	Subsystems int64   `json:"subsystems"`
	Enabled    int64   `json:"enabled"`
	Disabled   int64   `json:"disabled"`
	Reporting  int64   `json:"reporting"` // enabled AND served ≥1 traced request in the window
	Requests   int64   `json:"requests"`
	Errors     int64   `json:"errors"`
	ErrorRate  float64 `json:"errorRate"` // percent (0..100)
}

// subsystemRow is one subsystem: what it is, whether it is on, and how it behaved.
// The last-error fields are FLAT rather than a nested object so the console can sort
// and filter the table on them like any other column.
type subsystemRow struct {
	Name     string   `json:"name"`
	Prefixes []string `json:"prefixes"`
	Enabled  bool     `json:"enabled"`

	Requests       int64   `json:"requests"`
	RequestsPerMin float64 `json:"requestsPerMin"`
	Errors         int64   `json:"errors"`
	ErrorRate      float64 `json:"errorRate"` // percent (0..100)
	LatencyP50Ms   float64 `json:"latencyP50Ms"`
	LatencyP95Ms   float64 `json:"latencyP95Ms"`
	LatencyP99Ms   float64 `json:"latencyP99Ms"`

	LastErrorAt      string `json:"lastErrorAt"`
	LastErrorRoute   string `json:"lastErrorRoute"`
	LastErrorStatus  string `json:"lastErrorStatus"`
	LastErrorMessage string `json:"lastErrorMessage"`
}

// subsystems answers GET /v1/admin/subsystems. ?range=24h|7d|30d bounds the telemetry
// window (default 30d) — the same enum, and the same helpers, as the o11y board.
func subsystems(s *cloud.Service[core.State], c *zip.Ctx) error {
	ctx := c.Context()
	rangeLabel := o11yRange(c.Query("range"))
	since := computeSince(rangeLabel)
	now := time.Now().UTC()

	// The inventory is authoritative and always available — it IS this process.
	rows, at := subsystemRows(cloud.Subsystems()), now.Format(time.RFC3339)
	payload := subsystemBoard{
		Range:   rangeLabel,
		Start:   since.Format(time.RFC3339),
		End:     at,
		Rows:    rows,
		Sources: []core.SourceStatus{core.SrcOf("mount-index", nil, len(rows), at)},
	}

	if !datastore.Ready() {
		payload.Sources = append(payload.Sources, core.SrcOf("traces", errTracesUnconfigured, 0, at))
		payload.Totals = subsystemTotals{}.from(rows)
		return core.OK(c, payload)
	}

	sinceTS := chTS(since)
	index := rowIndex(rows)

	redRows, err := datastore.Query(ctx, subsystemREDSQL(), sinceTS)
	if err == nil {
		applyRED(rows, index, redRows, now.Sub(since))
	}
	payload.Sources = append(payload.Sources, core.SrcOf("traces", err, len(redRows), at))

	// Last error is a SEPARATE read so a failure here costs only that column — the
	// rates and percentiles above stay authoritative.
	errRows, lastErr := datastore.Query(ctx, subsystemLastErrorSQL(), sinceTS)
	if lastErr == nil {
		applyLastError(rows, index, errRows)
	}
	payload.Sources = append(payload.Sources, core.SrcOf("trace-errors", lastErr, len(errRows), at))

	payload.Totals = subsystemTotals{}.from(rows)
	return core.OK(c, payload)
}

// ── pure builders and folds (unit-tested) ──

// subsystemRows projects the mount index into board rows — the row set is the
// inventory, so a subsystem that served no traffic still appears (as a real zero)
// instead of vanishing from the board.
func subsystemRows(inv []cloud.Subsystem) []subsystemRow {
	out := make([]subsystemRow, 0, len(inv))
	for _, sub := range inv {
		prefixes := sub.Prefixes
		if prefixes == nil {
			prefixes = []string{} // JSON [] not null — the console renders a list
		}
		out = append(out, subsystemRow{Name: sub.Name, Prefixes: prefixes, Enabled: sub.Enabled})
	}
	return out
}

// rowIndex maps subsystem name → row position for the O(1) join below.
func rowIndex(rows []subsystemRow) map[string]int {
	idx := make(map[string]int, len(rows))
	for i, r := range rows {
		idx[r.Name] = i
	}
	return idx
}

// applyRED folds the warehouse aggregate onto the inventory rows. A name the mount
// index does not know is DROPPED: this board describes THIS binary, and a row for a
// subsystem it does not have (a rename, or a different build sharing the warehouse)
// would be a claim the process cannot stand behind.
func applyRED(rows []subsystemRow, index map[string]int, warehouse []map[string]any, window time.Duration) {
	minutes := window.Minutes()
	for _, r := range warehouse {
		i, ok := index[chStr(r["subsystem"])]
		if !ok {
			continue
		}
		row := &rows[i]
		row.Requests = chInt64(r["requests"])
		row.Errors = chInt64(r["errors"])
		row.ErrorRate = chFloat64(r["error_rate"])
		row.LatencyP50Ms = chFloat64(r["p50"])
		row.LatencyP95Ms = chFloat64(r["p95"])
		row.LatencyP99Ms = chFloat64(r["p99"])
		if minutes > 0 {
			row.RequestsPerMin = round2(float64(row.Requests) / minutes)
		}
	}
}

// applyLastError folds the most recent errored span per subsystem onto its row.
func applyLastError(rows []subsystemRow, index map[string]int, warehouse []map[string]any) {
	for _, r := range warehouse {
		i, ok := index[chStr(r["subsystem"])]
		if !ok {
			continue
		}
		row := &rows[i]
		row.LastErrorAt = chTime(r["at"])
		row.LastErrorRoute = chStr(r["route"])
		row.LastErrorStatus = chStr(r["status"])
		row.LastErrorMessage = chStr(r["message"])
	}
}

// from folds the rows into the KPI band. Reporting counts subsystems that are ON and
// actually served traced traffic — the gap against Enabled is the operator's signal
// that something mounted is receiving nothing.
func (subsystemTotals) from(rows []subsystemRow) subsystemTotals {
	var t subsystemTotals
	t.Subsystems = int64(len(rows))
	for _, r := range rows {
		if r.Enabled {
			t.Enabled++
		} else {
			t.Disabled++
		}
		if r.Enabled && r.Requests > 0 {
			t.Reporting++
		}
		t.Requests += r.Requests
		t.Errors += r.Errors
	}
	if t.Requests > 0 {
		t.ErrorRate = round2(100 * float64(t.Errors) / float64(t.Requests))
	}
	return t
}

// round2 trims a derived rate to 2dp so the wire carries a stable, legible number
// rather than a float artefact.
func round2(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }

// ── SQL builders (static SQL + one positional time bound; unit-tested) ──

// subsystemREDSQL is the per-subsystem rate/errors/duration aggregate.
func subsystemREDSQL() string {
	return "SELECT " + subsystemAttr + " AS subsystem, " +
		"count() AS requests, " +
		"countIf(has_error) AS errors, " +
		"round(100 * countIf(has_error) / greatest(count(), 1), 3) AS error_rate, " +
		"round(quantile(0.5)(" + o11yDurationCol + ") / 1e6, 2) AS p50, " +
		"round(quantile(0.95)(" + o11yDurationCol + ") / 1e6, 2) AS p95, " +
		"round(quantile(0.99)(" + o11yDurationCol + ") / 1e6, 2) AS p99 " +
		"FROM " + o11yTraceTable + " WHERE timestamp >= ? AND " + subsystemAttr + " != '' " +
		"GROUP BY subsystem"
}

// subsystemLastErrorSQL is the most recent errored span per subsystem: when, on which
// route, with what status and message. argMax(…, timestamp) picks the newest row's
// value in the same pass that max(timestamp) dates it.
func subsystemLastErrorSQL() string {
	return "SELECT " + subsystemAttr + " AS subsystem, " +
		"max(timestamp) AS at, " +
		"argMax(attributes_string['http.route'], timestamp) AS route, " +
		"argMax(response_status_code, timestamp) AS status, " +
		"argMax(status_message, timestamp) AS message " +
		"FROM " + o11yTraceTable + " WHERE timestamp >= ? AND has_error AND " + subsystemAttr + " != '' " +
		"GROUP BY subsystem"
}
