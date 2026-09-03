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
// event.span table, over the SAME datastore client, as the o11y board beside it.
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
	"context"
	"errors"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/datastore"
)

// errTracesUnconfigured marks the trace warehouse absent on this deployment. core.SrcOf
// renders it as a not-ok source, so the board says "telemetry unavailable" beside a
// still-truthful inventory instead of implying every subsystem served zero requests.
var errTracesUnconfigured = errors.New("trace warehouse not connected")

// subsystemAttr is the span attribute cloud.TracingMiddleware stamps — the ONE label
// that separates the co-resident subsystems. Spelled once here and reused by every
// query below, so the reader and the writer cannot drift apart on the key.
const subsystemAttr = "attributes['hanzo.subsystem']"

// subsystemBoard is the whole per-subsystem board payload.
type subsystemBoard struct {
	// Range is the telemetry window the RED columns cover: 24h, 7d or 30d,
	// normalized the same way the o11y board normalizes it. It does NOT bound the
	// inventory — a subsystem appears whether or not it served anything.
	Range string `json:"range"`
	// Start is that window's lower bound, RFC 3339 UTC.
	Start string `json:"start"`
	// End is the moment of this read, RFC 3339 UTC.
	End string `json:"end"`
	// Totals is the KPI band above the table.
	Totals subsystemTotals `json:"totals"`
	// Rows is one row per subsystem MOUNTED IN THIS PROCESS, in the composition
	// root's order. The row set comes from the mount index, not from the
	// warehouse, so a subsystem that served nothing appears as a real zero instead
	// of vanishing.
	Rows []subsystemRow `json:"rows"`
	// Sources is one row per input: mount-index (always ok — it IS this process),
	// traces, and trace-errors. The last two go not-ok when the warehouse is
	// absent, which is what says "telemetry unavailable" instead of implying every
	// subsystem served nothing.
	Sources []core.SourceStatus `json:"sources"`
}

// subsystemTotals is the KPI band above the table.
type subsystemTotals struct {
	// Subsystems is how many are mounted in this binary — the row count.
	Subsystems int64 `json:"subsystems"`
	// Enabled is how many of them are switched ON.
	Enabled int64 `json:"enabled"`
	// Disabled is the rest. With Enabled it sums to Subsystems.
	Disabled  int64 `json:"disabled"`
	Reporting int64 `json:"reporting"` // enabled AND served ≥1 traced request in the window
	// Requests is the traced requests every subsystem served in the window,
	// summed.
	Requests int64 `json:"requests"`
	// Errors is how many of them errored.
	Errors    int64   `json:"errors"`
	ErrorRate float64 `json:"errorRate"` // percent (0..100)
}

// subsystemRow is one subsystem: what it is, whether it is on, and how it behaved.
// The last-error fields are FLAT rather than a nested object so the console can sort
// and filter the table on them like any other column.
type subsystemRow struct {
	// Name is the subsystem's name in the mount index, and the label the request
	// span is stamped with. It is the join key between the inventory and the
	// warehouse: a warehouse row naming something this binary does not mount is
	// dropped rather than shown.
	Name string `json:"name"`
	// Prefixes is the route prefixes it answers on, e.g. /v1/admin. Always a list,
	// never null — empty means it mounts no HTTP routes of its own.
	Prefixes []string `json:"prefixes"`
	// Enabled is whether it is switched ON in this process. Process-local and
	// always truthful — it needs no warehouse — which is what answers "is that row
	// empty because it is broken or because it is off".
	Enabled bool `json:"enabled"`

	// Requests is the traced requests it served in the window. Zero when the
	// warehouse is unavailable, so read the board's sources before reading it as
	// idle.
	Requests int64 `json:"requests"`
	// RequestsPerMin is Requests averaged over the WHOLE window, to 2dp — a flat
	// mean, so a burst and a steady trickle of the same volume look alike here.
	RequestsPerMin float64 `json:"requestsPerMin"`
	// Errors is how many of those requests errored.
	Errors    int64   `json:"errors"`
	ErrorRate float64 `json:"errorRate"` // percent (0..100)
	// LatencyP50Ms is its median request duration in milliseconds.
	LatencyP50Ms float64 `json:"latencyP50Ms"`
	// LatencyP95Ms is its 95th percentile in milliseconds.
	LatencyP95Ms float64 `json:"latencyP95Ms"`
	// LatencyP99Ms is its 99th percentile in milliseconds.
	LatencyP99Ms float64 `json:"latencyP99Ms"`

	// LastErrorAt is when this subsystem last errored in the window, RFC 3339 UTC.
	// Empty when it did not — or when the last-error read failed, which the
	// trace-errors source reports.
	LastErrorAt string `json:"lastErrorAt"`
	// LastErrorRoute is the route pattern that error was on, e.g. /v1/admin/orgs.
	// Empty when the span carried none.
	LastErrorRoute string `json:"lastErrorRoute"`
	// LastErrorStatus is the HTTP status code it answered, as a STRING — it is a
	// span attribute, and attribute values arrive as text.
	LastErrorStatus string `json:"lastErrorStatus"`
	// LastErrorMessage is the span's status message. Empty when the span recorded
	// none, which is common: a span can be marked errored without a message.
	LastErrorMessage string `json:"lastErrorMessage"`
}

// SubsystemsIn is the GET /v1/admin/subsystems filter.
type SubsystemsIn struct {
	// Range bounds the telemetry window: 24h, 7d or 30d. Anything else, including
	// empty, resolves to the default through the same window grammar the o11y board uses.
	Range string `json:"range"`
}

// SubsystemsOut is the GET /v1/admin/subsystems envelope.
type SubsystemsOut struct {
	// Status is "ok" or "error", at HTTP 200 either way. An absent warehouse still
	// answers "ok" with the full inventory — the telemetry gap is reported in
	// data.sources.
	Status string `json:"status"`
	// Msg is the failure reason when Status is "error", empty otherwise.
	Msg string `json:"msg"`
	// Data is the board. Null only when the caller was refused.
	Data *subsystemBoard `json:"data,omitempty"`
}

// subsystems answers GET /v1/admin/subsystems. ?range=24h|7d|30d bounds the telemetry
// window (default 30d) — the same enum, and the same helpers, as the o11y board.
func (o ops) Subsystems(ctx context.Context, in *SubsystemsIn) (*SubsystemsOut, error) {
	if _, err := core.Admit(ctx); err != nil {
		return nil, err
	}
	rangeLabel := core.WarehouseRange(in.Range)
	since := core.WarehouseSince(rangeLabel)
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
		return &SubsystemsOut{Status: core.OK, Data: &payload}, nil
	}

	sinceTS := core.CHTimeLit(since)
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
	return &SubsystemsOut{Status: core.OK, Data: &payload}, nil
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
		i, ok := index[core.CHStr(r["subsystem"])]
		if !ok {
			continue
		}
		row := &rows[i]
		row.Requests = core.CHInt64(r["requests"])
		row.Errors = core.CHInt64(r["errors"])
		row.ErrorRate = core.CHFloat64(r["error_rate"])
		row.LatencyP50Ms = core.CHFloat64(r["p50"])
		row.LatencyP95Ms = core.CHFloat64(r["p95"])
		row.LatencyP99Ms = core.CHFloat64(r["p99"])
		if minutes > 0 {
			row.RequestsPerMin = round2(float64(row.Requests) / minutes)
		}
	}
}

// applyLastError folds the most recent errored span per subsystem onto its row.
func applyLastError(rows []subsystemRow, index map[string]int, warehouse []map[string]any) {
	for _, r := range warehouse {
		i, ok := index[core.CHStr(r["subsystem"])]
		if !ok {
			continue
		}
		row := &rows[i]
		row.LastErrorAt = core.CHTime(r["at"])
		row.LastErrorRoute = core.CHStr(r["route"])
		row.LastErrorStatus = core.CHStr(r["status"])
		row.LastErrorMessage = core.CHStr(r["message"])
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
		"countIf(status = 'error') AS errors, " +
		"round(100 * countIf(status = 'error') / greatest(count(), 1), 3) AS error_rate, " +
		"round(quantile(0.5)(" + o11yDurationCol + ") / 1e6, 2) AS p50, " +
		"round(quantile(0.95)(" + o11yDurationCol + ") / 1e6, 2) AS p95, " +
		"round(quantile(0.99)(" + o11yDurationCol + ") / 1e6, 2) AS p99 " +
		"FROM " + o11yTraceTable + " WHERE time >= ? AND " + subsystemAttr + " != '' " +
		"GROUP BY subsystem"
}

// subsystemLastErrorSQL is the most recent errored span per subsystem: when, on which
// route, with what status and message. argMax(…, time) picks the newest row's value
// in the same pass that max(time) dates it. The HTTP facts are span attributes on
// the plane; status.message is where the plane sink folds a span's status message.
func subsystemLastErrorSQL() string {
	return "SELECT " + subsystemAttr + " AS subsystem, " +
		"max(time) AS at, " +
		"argMax(attributes['http.route'], time) AS route, " +
		"argMax(attributes['http.response.status_code'], time) AS status, " +
		"argMax(attributes['status.message'], time) AS message " +
		"FROM " + o11yTraceTable + " WHERE time >= ? AND status = 'error' AND " + subsystemAttr + " != '' " +
		"GROUP BY subsystem"
}
