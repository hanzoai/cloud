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
// price_cents, ts) — the same warehouse (`datastore`, datastore) the analytics
// subsystem reads, over the SAME shared client (datastore.Query), no
// second connection. `kind` is an OPEN LowCardinality spectrum (bot | machine |
// cluster | nodepool | container | function | …) — a bot is a machine running the
// @hanzo/bot agent, a machine is raw compute visor opens — and each console lens
// reuses this one endpoint with a different `?kind=` (Bots=bot, Machines=machine).
//
// SUPERADMIN ONLY (core.Admit, the op's first line), all-orgs by default; this is
// an AGGREGATOR — admin holds no compute state, it only reads. Honest by
// construction, exactly like the analytics events lens: no datastore connected, or
// the events table not provisioned yet (the emitter is still being wired) → the
// real empty list, NEVER a fabricated fleet. admin creates NO table (the datastore
// stream owns hanzo.compute_usage). Money is USD cents end to end.

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/apps/datastore"
)

// computeTable is the operator-owned compute-usage warehouse table (named to match
// the existing hanzo.cloud_usage convention; the visor/commerce emitter writes it).
// admin only READS it (never creates it — mirrors how analytics treats event.event).
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
	// Org is the tenant these workloads belong to.
	Org string `json:"org"`
	// App is the application within that tenant, as the emitter recorded it.
	App string `json:"app"`
	// Project is the project within that app. With Org and App and Kind it forms
	// the group this row folds — the console's org to app to project tree is these
	// four columns.
	Project string `json:"project"`
	// Kind is the workload class: bot, machine, cluster, nodepool, container,
	// function and whatever else the emitter writes. An OPEN set, lowercased, not
	// an enum — a bot is a machine running the agent, a machine is raw compute —
	// so match it as a string and expect values this list does not have.
	Kind string `json:"kind"`
	// Machines is how many DISTINCT machines of that kind ran in the window,
	// counted by machine id. Not a count of events.
	Machines int64 `json:"machines"`
	// Active is how many of them are still running — the ones whose LATEST
	// lifecycle event is not a terminal one (stop, destroy, terminate, delete,
	// off, shutdown, expire and their past tenses). Decided in the warehouse over
	// every machine, not over a page.
	Active int64 `json:"active"`
	// SpendCents is what the group billed over the window, in US cents, summed
	// from the per-event prices.
	SpendCents int64 `json:"spendCents"`
	// LastTs is the most recent event in the group, RFC 3339 UTC. For a group with
	// no active machines it is when the last one stopped.
	LastTs string `json:"lastTs"`
}

// compute rolls the fleet's compute usage up to one row per (org, app, project, kind):
// how many distinct machines ran in the window, how many are still active, what they
// billed, and when each group last emitted an event. The console folds these into its
// org → app → project tree.
//
// A machine counts as ACTIVE when its LATEST lifecycle event is not a terminal one
// (stop/destroy/terminate/delete/off/shutdown/expire and their past tenses) — the same
// fold the console applies, done in the warehouse so the count is over every machine and
// not just the page.
//
// Honest-empty when the warehouse is not connected or hanzo.compute_usage is not
// provisioned yet: an empty list, never a fabricated fleet.
//
// Example: {"kind":"bot","org":"acme","range":"7d"}
// Response: {"status":"ok","msg":"","data":[{"org":"acme","app":"support","project":"default",
// "kind":"bot","machines":4,"active":2,"spendCents":900,"lastTs":"2026-07-26T18:00:00Z"}],"total":1}
func compute(ctx context.Context, in *computeIn) (*computeOut, error) {
	if _, err := core.Admit(ctx); err != nil {
		return nil, err
	}
	// Honest-empty when the warehouse is not connected or the usage table is not
	// provisioned yet (the visor/commerce emitter is still being wired).
	if !datastore.Ready() || !core.CHTableExists(ctx, computeTable) {
		return &computeOut{Status: core.OK, Data: []computeLeaf{}, Total: core.Total(0)}, nil
	}

	// `kind` is an OPEN LowCardinality spectrum (bot | machine | cluster | nodepool |
	// container | function | …), matched as a PLAIN STRING — no enum assumption. Each
	// console lens passes its own kind; empty = all kinds. Case-normalized to the
	// warehouse's lower-case convention.
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	sql, args := buildComputeQuery(in.Range, kind, strings.TrimSpace(in.Org))
	rows, err := datastore.Query(ctx, sql, args...)
	if err != nil {
		return &computeOut{Status: core.Err, Msg: "compute query: " + err.Error()}, nil
	}
	leaves := computeLeavesFromRows(rows)
	return &computeOut{Status: core.OK, Data: leaves, Total: core.Total(len(leaves))}, nil
}

// computeIn is the GET /v1/admin/compute query.
type computeIn struct {
	// Kind narrows to one workload class (bot | machine | cluster | nodepool |
	// container | function | …). An OPEN spectrum matched as a plain string, lowercased
	// to the warehouse's convention; empty means every kind.
	Kind string `json:"kind"`
	// Org narrows to one tenant. Empty means every tenant — this board is
	// cross-tenant by nature.
	Org string `json:"org"`
	// Range is the lower time bound: 24h, 7d or 30d. Anything else reads as 30d.
	Range string `json:"range"`
}

// computeOut is the GET /v1/admin/compute envelope. total == len(data): the roll-up is
// one row per group, unpaginated.
type computeOut struct {
	// Status is "ok" or "error", at HTTP 200 either way.
	Status string `json:"status"`
	// Msg is the failure reason when Status is "error" — the warehouse rejected
	// the query. A warehouse that is not connected, or a usage table not
	// provisioned yet, is a SUCCESS carrying an empty list instead.
	Msg string `json:"msg"`
	// Data is one row per group, biggest spender first. Null when Status is
	// "error"; empty when there is genuinely nothing, or when the warehouse is
	// unavailable.
	Data []computeLeaf `json:"data"`
	// Total is len(data): the roll-up is one row per group, unpaginated. Absent
	// when the read failed.
	Total *int `json:"total,omitempty"`
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
	args := []any{core.CHTimeLit(core.WarehouseSince(rangeLabel))}
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
		"argMax(event, ts) NOT IN (" + core.SQLInList(terminalComputeEvents) + ") AS active " +
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
			Org:        core.CHStr(r["org"]),
			App:        core.CHStr(r["app"]),
			Project:    core.CHStr(r["project"]),
			Kind:       core.CHStr(r["kind"]),
			Machines:   core.CHInt64(r["machines"]),
			Active:     core.CHInt64(r["active"]),
			SpendCents: core.CHInt64(r["spend_cents"]),
			LastTs:     core.CHTime(r["last_ts"]),
		})
	}
	return leaves
}
