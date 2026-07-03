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

import (
	"strings"
	"testing"
	"time"
)

// TestBuildComputeQuery_Filters proves the WHERE clause binds the range, and adds
// kind + org as POSITIONAL params only when supplied — never interpolated.
func TestBuildComputeQuery_Filters(t *testing.T) {
	// Unfiltered: time bound only.
	sql, args := buildComputeQuery("30d", "", "")
	if len(args) != 1 {
		t.Fatalf("unfiltered args = %d, want 1 (time bound)", len(args))
	}
	// Reads the real table the datastore stream + visor emitter write.
	if !strings.Contains(sql, "FROM hanzo.compute_usage") {
		t.Errorf("query must read hanzo.compute_usage; got %q", sql)
	}
	if !strings.Contains(sql, "GROUP BY org, app, project, kind") {
		t.Errorf("query must group by (org, app, project, kind); got %q", sql)
	}
	if strings.Contains(sql, "AND kind = ?") || strings.Contains(sql, "AND org = ?") {
		t.Errorf("unfiltered query must not add kind/org predicates; got %q", sql)
	}

	// kind=bot + org: two extra bound params, in order.
	sql, args = buildComputeQuery("7d", "bot", "acme")
	if len(args) != 3 {
		t.Fatalf("kind+org args = %d, want 3", len(args))
	}
	if args[1] != "bot" || args[2] != "acme" {
		t.Errorf("args = %v, want [<ts> bot acme]", args)
	}
	if !strings.Contains(sql, "AND kind = ?") || !strings.Contains(sql, "AND org = ?") {
		t.Errorf("filtered query must bind kind + org; got %q", sql)
	}

	// OPEN SPECTRUM: an arbitrary kind (not bot/machine) filters too — no enum
	// whitelist. Future Clusters/Functions lenses reuse this endpoint unchanged.
	sql, args = buildComputeQuery("30d", "cluster", "")
	if len(args) != 2 || args[1] != "cluster" {
		t.Fatalf("kind=cluster args = %v, want [<ts> cluster]", args)
	}
	if !strings.Contains(sql, "AND kind = ?") {
		t.Errorf("an arbitrary kind must still bind the kind predicate; got %q", sql)
	}
}

// TestComputeSince maps the range enum to a lower time bound (default 30d).
func TestComputeSince(t *testing.T) {
	now := time.Now().UTC()
	cases := map[string]time.Duration{
		"24h": 24 * time.Hour,
		"7d":  7 * 24 * time.Hour,
		"30d": 30 * 24 * time.Hour,
		"":    30 * 24 * time.Hour, // default
		"xyz": 30 * 24 * time.Hour, // unknown → default
	}
	for label, want := range cases {
		got := now.Sub(computeSince(label))
		if d := got - want; d < -2*time.Second || d > 2*time.Second {
			t.Errorf("computeSince(%q) lookback = %v, want ≈%v", label, got, want)
		}
	}
}

// TestTerminalComputeSQL renders the terminal set as a quoted CH list.
func TestTerminalComputeSQL(t *testing.T) {
	got := terminalComputeSQL()
	for _, e := range []string{"'stop'", "'destroy'", "'terminated'", "'shutdown'"} {
		if !strings.Contains(got, e) {
			t.Errorf("terminal list missing %s; got %q", e, got)
		}
	}
	if strings.Contains(got, "'start'") || strings.Contains(got, "'provision'") {
		t.Errorf("terminal list must NOT include running states; got %q", got)
	}
}

// TestComputeLeavesFromRows maps the driver's native row types (uint64 counts,
// time.Time DateTime, string dims) onto typed leaves — the honest-empty and the
// real-row paths both.
func TestComputeLeavesFromRows(t *testing.T) {
	if got := computeLeavesFromRows(nil); len(got) != 0 {
		t.Fatalf("nil rows → %d leaves, want 0", len(got))
	}
	ts := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	rows := []map[string]any{
		{
			"org": "acme", "app": "web", "project": "prod", "kind": "machine",
			"machines": uint64(4), "active": uint64(3), "spend_cents": uint64(1200), "last_ts": ts,
		},
	}
	got := computeLeavesFromRows(rows)
	if len(got) != 1 {
		t.Fatalf("rows → %d leaves, want 1", len(got))
	}
	l := got[0]
	if l.Org != "acme" || l.App != "web" || l.Project != "prod" || l.Kind != "machine" {
		t.Errorf("dims wrong: %+v", l)
	}
	if l.Machines != 4 || l.Active != 3 || l.SpendCents != 1200 {
		t.Errorf("counts wrong: %+v", l)
	}
	if l.LastTs != "2026-07-01T12:00:00Z" {
		t.Errorf("lastTs = %q, want 2026-07-01T12:00:00Z", l.LastTs)
	}
}

// TestComputeCoercers proves the map coercers accept the driver natives + degrade.
func TestComputeCoercers(t *testing.T) {
	if chInt64(uint64(7)) != 7 || chInt64(int64(7)) != 7 || chInt64(float64(7)) != 7 {
		t.Error("chInt64 must accept uint64/int64/float64")
	}
	if chInt64("nope") != 0 || chInt64(nil) != 0 {
		t.Error("chInt64 must degrade non-numerics to 0")
	}
	if chStr("x") != "x" || chStr(42) != "" {
		t.Error("chStr must pass strings, degrade others to empty")
	}
	if chTime(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) != "2026-01-02T03:04:05Z" {
		t.Error("chTime must format time.Time as RFC3339 UTC")
	}
	if chTime(123) != "" {
		t.Error("chTime must degrade non-time to empty")
	}
}
