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

// compute — GET /v1/admin/compute, the cross-tenant compute-analytics read the
// operator's Bots and Machines boards (admin.hanzo.ai) group into an
// org → app → project tree. It aggregates the operator-owned usage table
// hanzo.compute_usage(org, app, project, kind, event, machine_id, size,
// price_cents, ts) — the same warehouse (`datastore`, ClickHouse) the analytics
// subsystem reads, over the SAME shared client (aiobject.DatastoreQuery), no
// second connection. `kind` is an OPEN LowCardinality spectrum (bot | machine |
// cluster | nodepool | container | function | …) — a bot is a machine running the
// @hanzo/bot agent, a machine is raw compute visor opens — and each console lens
// reuses this one endpoint with a different `?kind=` (Bots=bot, Machines=machine).
//
// SUPERADMIN ONLY (the s.guard wrap in admin.go), all-orgs by default; this is
// an AGGREGATOR — admin holds no compute state, it only reads. Honest by
// construction, exactly like the analytics events lens: no datastore connected, or
// the events table not provisioned yet (the emitter is still being wired) → the
// real empty list, NEVER a fabricated fleet. admin creates NO table (the datastore
// stream owns hanzo.compute_usage). Money is USD cents end to end.

import (
	"context"
	"strings"
	"time"

	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/zap-proto/zip"
)

// computeTable is the operator-owned compute-usage warehouse table (named to match
// the existing hanzo.cloud_usage convention; the visor/commerce emitter writes it).
// admin only READS it (never creates it — mirrors how analytics treats hanzo.events).
const computeTable = "hanzo.compute_usage"

// terminalComputeEvents are the lifecycle events whose LATEST occurrence means a
// machine is no longer running (mirrors the console foldEvents terminal set). A
// CLOSED server-side constant — never user input — so rendering it into the argMax
// check is injection-safe.
var terminalComputeEvents = []string{
	"stop", "stopped", "destroy", "destroyed", "terminate", "terminated",
	"delete", "deleted", "off", "shutdown", "expire", "expired",
}

// computeLeaf is one (org, app, project, kind) rollup: distinct machines of that
// kind, how many are currently active (latest event non-terminal), the billed
// spend over the window, and the most recent event. The console folds these into
// the org → app → project tree.
type computeLeaf struct {
	Org        string `json:"org"`
	App        string `json:"app"`
	Project    string `json:"project"`
	Kind       string `json:"kind"`
	Machines   int64  `json:"machines"`
	Active     int64  `json:"active"`
	SpendCents int64  `json:"spendCents"`
	LastTs     string `json:"lastTs"`
}

// compute answers GET /v1/admin/compute. ?kind=<kind> and ?org= narrow the
// aggregate; ?range=24h|7d|30d bounds it (default 30d). SuperAdmin only.
func compute(s *cloud.Service[core.State], c *zip.Ctx) error {
	ctx := c.Context()
	// Honest-empty when the warehouse is not connected or the usage table is not
	// provisioned yet (the visor/commerce emitter is still being wired).
	if !aiobject.DatastoreEnabled() || !computeTableExists(ctx) {
		return core.OKList(c, []computeLeaf{}, 0)
	}

	// `kind` is an OPEN LowCardinality spectrum (bot | machine | cluster | nodepool |
	// container | function | …), matched as a PLAIN STRING — no enum assumption. Each
	// console lens passes its own kind; empty = all kinds. Case-normalized to the
	// warehouse's lower-case convention.
	kind := strings.ToLower(strings.TrimSpace(c.Query("kind")))
	sql, args := buildComputeQuery(c.Query("range"), kind, strings.TrimSpace(c.Query("org")))
	rows, err := aiobject.DatastoreQuery(ctx, sql, args...)
	if err != nil {
		return core.Fail(c, "compute query: "+err.Error())
	}
	leaves := computeLeavesFromRows(rows)
	return core.OKList(c, leaves, len(leaves))
}

// buildComputeQuery assembles the two-level roll-up (pure, so it is unit-tested).
// The inner query resolves each machine's LATEST lifecycle state (argMax event by
// ts) + its billed spend; the outer counts machines, counts the still-active ones,
// and sums spend per (org, app, project, kind). `kind` is a PLAIN STRING over an
// open LowCardinality spectrum (no enum assumption; any non-empty value filters) and
// the terminal set is a constant, so nothing user-derived is interpolated — org,
// kind, and the time bound are all POSITIONAL parameters.
func buildComputeQuery(rangeLabel, kind, org string) (string, []any) {
	where := "ts >= ?"
	args := []any{chTS(computeSince(rangeLabel))}
	if kind != "" {
		where += " AND kind = ?"
		args = append(args, kind)
	}
	if org != "" {
		where += " AND org = ?"
		args = append(args, org)
	}
	sql := "SELECT org, app, project, kind, " +
		"count() AS machines, countIf(active) AS active, sum(spend) AS spend_cents, max(last_ts) AS last_ts " +
		"FROM (SELECT org, app, project, kind, machine_id, " +
		"sum(price_cents) AS spend, max(ts) AS last_ts, " +
		"argMax(event, ts) NOT IN (" + terminalComputeSQL() + ") AS active " +
		"FROM " + computeTable + " WHERE " + where + " " +
		"GROUP BY org, app, project, kind, machine_id) " +
		"GROUP BY org, app, project, kind ORDER BY spend_cents DESC"
	return sql, args
}

// computeLeavesFromRows maps the DatastoreQuery rows onto []computeLeaf (pure).
func computeLeavesFromRows(rows []map[string]any) []computeLeaf {
	leaves := make([]computeLeaf, 0, len(rows))
	for _, r := range rows {
		leaves = append(leaves, computeLeaf{
			Org:        chStr(r["org"]),
			App:        chStr(r["app"]),
			Project:    chStr(r["project"]),
			Kind:       chStr(r["kind"]),
			Machines:   chInt64(r["machines"]),
			Active:     chInt64(r["active"]),
			SpendCents: chInt64(r["spend_cents"]),
			LastTs:     chTime(r["last_ts"]),
		})
	}
	return leaves
}

// computeTableExists probes for the operator-owned events table. Any error → false
// (honest "not available yet"), mirroring analytics.tableExists.
func computeTableExists(ctx context.Context) bool {
	rows, err := aiobject.DatastoreQuery(ctx, "EXISTS TABLE "+computeTable)
	if err != nil || len(rows) == 0 {
		return false
	}
	for _, v := range rows[0] {
		return chInt64(v) == 1
	}
	return false
}

// computeSince maps the ?range enum to a lower time bound (default 30d).
func computeSince(rangeLabel string) time.Time {
	now := time.Now().UTC()
	switch strings.TrimSpace(rangeLabel) {
	case "24h":
		return now.Add(-24 * time.Hour)
	case "7d":
		return now.Add(-7 * 24 * time.Hour)
	default:
		return now.Add(-30 * 24 * time.Hour)
	}
}

// terminalComputeSQL renders the terminal-event set as a ClickHouse string list.
func terminalComputeSQL() string {
	quoted := make([]string, len(terminalComputeEvents))
	for i, e := range terminalComputeEvents {
		quoted[i] = "'" + e + "'"
	}
	return strings.Join(quoted, ",")
}

// chTS formats a time as a ClickHouse DateTime literal (UTC), bound as a string arg.
func chTS(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }

// ── map[string]any coercers (the DatastoreQuery row shape) ───────────────────
//
// The ClickHouse driver decodes each column to its native Go type (uint64 for
// count()/sum(UInt*), time.Time for DateTime, string for String); these accept
// those natives so a driver/transport change can't crash a read.

func chInt64(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case int32:
		return int64(n)
	case uint:
		return int64(n)
	case uint64:
		return int64(n)
	case uint32:
		return int64(n)
	case uint16:
		return int64(n)
	case uint8:
		return int64(n)
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	default:
		return 0
	}
}

func chStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// chTime coerces a ClickHouse DateTime (time.Time) to an RFC3339 UTC string.
func chTime(v any) string {
	switch t := v.(type) {
	case time.Time:
		return t.UTC().Format(time.RFC3339)
	case string:
		return t
	default:
		return ""
	}
}
