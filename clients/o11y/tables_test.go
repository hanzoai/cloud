// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
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

package o11y

import (
	"strings"
	"testing"
)

// deadNames are the retired schema generation's identifiers: the old table names and
// the compat ALIAS columns that go with them. A read that still carries one of these
// answers HTTP 200 with zero rows against the v1 schema — silent, un-logged, and
// exactly the write/read split this tripwire exists to catch.
var deadNames = []string{
	"o11y_index_v3", "logs_v2", "traces_v3_resource", "tag_attributes_v2",
	"durationNano", "serviceName", "hasError", "httpRoute",
}

// TestReadsCanonicalTables proves every direct-SQL read in this package names the
// canonical v1 table — one generation, no version suffixes — and carries no retired
// name. The writer is the collector; the two must be bumped in the same commit.
func TestReadsCanonicalTables(t *testing.T) {
	svc := service{ID: "chat", App: "chat"}
	infraCursor, _ := infraLogsSQL("chat", 1700000000000000000, 900, 200)
	infraWindow, _ := infraLogsSQL("chat", 0, 900, 200)
	requestCursor, _ := requestLogsSQL("acme", svc, 1700000000000000000, 900, 200)
	requestWindow, _ := requestLogsSQL("acme", svc, 0, 900, 200)
	redTenant, _ := redSeriesSQL(metricsQuery{svc: svc, org: "acme", rangeSec: 3600, stepSec: 60})
	redAdmin, _ := redSeriesSQL(metricsQuery{svc: svc, admin: true, rangeSec: 3600, stepSec: 60})

	cases := []struct{ name, sql, table string }{
		{"infraLogs/cursor", infraCursor, logTable},
		{"infraLogs/window", infraWindow, logTable},
		{"requestLogs/cursor", requestCursor, spanTable},
		{"requestLogs/window", requestWindow, spanTable},
		{"redSeries/tenant", redTenant, spanTable},
		{"redSeries/admin", redAdmin, spanTable},
	}
	for _, c := range cases {
		if !strings.Contains(c.sql, "FROM "+c.table) {
			t.Errorf("%s must read %s; got %q", c.name, c.table, c.sql)
		}
		for _, dead := range deadNames {
			if strings.Contains(c.sql, dead) {
				t.Errorf("%s names the retired %q; got %q", c.name, dead, c.sql)
			}
		}
	}

	if logTable != "o11y_logs.distributed_records" {
		t.Errorf("logTable = %q, want o11y_logs.distributed_records", logTable)
	}
	if spanTable != "o11y_traces.distributed_spans" {
		t.Errorf("spanTable = %q, want o11y_traces.distributed_spans", spanTable)
	}
}

// TestSpanReadsRealColumns proves the span reads name the REAL materialized columns
// (backtick-quoted, `$$` and all) and alias the route to the key the row parser
// reads — not the retired generation's ALIAS block.
func TestSpanReadsRealColumns(t *testing.T) {
	svc := service{ID: "chat", App: "chat"}
	q, _ := requestLogsSQL("acme", svc, 0, 900, 200)
	if !strings.Contains(q, spanRoute+" AS http_route") {
		t.Errorf("requestLogs must alias the route column to http_route; got %q", q)
	}
	if !strings.Contains(q, spanService+" = ?") {
		t.Errorf("requestLogs must match the service column; got %q", q)
	}
	red, _ := redSeriesSQL(metricsQuery{svc: svc, org: "acme", rangeSec: 3600, stepSec: 60})
	if !strings.Contains(red, spanRoute) || !strings.Contains(red, spanService) {
		t.Errorf("redSeries must scope on the real route/service columns; got %q", red)
	}
	if !strings.Contains(red, "duration_nano") {
		t.Errorf("redSeries must read duration_nano; got %q", red)
	}
}

// TestTenantGateIsBound proves the org predicate is a BOUND parameter that a
// non-admin always carries, and that an admin read drops it (fleet view) — the
// isolation invariant must survive the table rename.
func TestTenantGateIsBound(t *testing.T) {
	svc := service{ID: "chat", App: "chat"}
	tenant, tenantArgs := redSeriesSQL(metricsQuery{svc: svc, org: "acme", rangeSec: 3600, stepSec: 60})
	if !strings.Contains(tenant, "attributes_string['hanzo.org'] = ?") {
		t.Errorf("a non-admin RED read must gate on the org; got %q", tenant)
	}
	if tenantArgs[len(tenantArgs)-1] != "acme" {
		t.Errorf("the org must be the last bound arg; got %v", tenantArgs)
	}
	admin, _ := redSeriesSQL(metricsQuery{svc: svc, admin: true, rangeSec: 3600, stepSec: 60})
	if strings.Contains(admin, "hanzo.org") {
		t.Errorf("a SuperAdmin RED read is fleet-wide (no org predicate); got %q", admin)
	}
	// The org is never interpolated: it appears only as a bound arg.
	if strings.Contains(tenant, "acme") {
		t.Errorf("the org must never be interpolated into the SQL; got %q", tenant)
	}

	q, args := requestLogsSQL("acme", svc, 0, 900, 200)
	if !strings.HasPrefix(strings.SplitN(q, "WHERE ", 2)[1], "attributes_string['hanzo.org'] = ?") {
		t.Errorf("the org must be the FIRST predicate on a request-log read; got %q", q)
	}
	if args[0] != "acme" || strings.Contains(q, "acme") {
		t.Errorf("the org must be bound, never interpolated; got %q / %v", q, args)
	}
}
