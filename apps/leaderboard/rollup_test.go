package leaderboard

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestEnsureUsageRollup_OrderAndLatch: the base ledger is ensured first, then the
// target table, then the MV; the projection is type-exact; a second call is a no-op.
func TestEnsureUsageRollup_OrderAndLatch(t *testing.T) {
	var stmts []string
	baseEnsured := false
	oe, oet := execDatastore, ensureUsageTable
	execDatastore = func(_ context.Context, stmt string, _ ...any) error { stmts = append(stmts, stmt); return nil }
	ensureUsageTable = func(context.Context) error { baseEnsured = true; return nil }
	rollupReady.Store(false)
	t.Cleanup(func() { execDatastore, ensureUsageTable = oe, oet; rollupReady.Store(false) })

	if err := EnsureUsageRollup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !baseEnsured {
		t.Fatal("cloud_usage base must be ensured before the MV references it")
	}
	want := 2 + len(rollupColumnMigrations)
	if len(stmts) != want {
		t.Fatalf("want table+migrations+mv (%d DDL), got %d: %v", want, len(stmts), stmts)
	}
	if !strings.Contains(stmts[0], "CREATE TABLE IF NOT EXISTS hanzo.usage_rollup_daily") || !strings.Contains(stmts[0], "SummingMergeTree") {
		t.Fatalf("target DDL wrong: %s", stmts[0])
	}
	// The migrations sit BETWEEN the table and the view, and every one is additive:
	// a statement here runs on every boot, so it may never rewrite or drop.
	for _, m := range stmts[1 : len(stmts)-1] {
		if !strings.Contains(m, "ADD COLUMN IF NOT EXISTS") {
			t.Fatalf("rollup migration is not additive: %s", m)
		}
	}
	mv := stmts[len(stmts)-1]
	if !strings.Contains(mv, "CREATE MATERIALIZED VIEW IF NOT EXISTS hanzo.usage_rollup_daily_mv") {
		t.Fatalf("mv DDL wrong: %s", mv)
	}
	// Type-exact projection so the MV can never fail a valid ledger insert.
	for _, tok := range []string{"toDate(timestamp) AS day", "toUInt64(count()) AS requests", "toUInt64(sum(total_tokens))", "toInt64(sum(cost_nano)) AS cost_nano", "GROUP BY day, organization, user_id, model"} {
		if !strings.Contains(mv, tok) {
			t.Fatalf("mv missing type-exact term %q: %s", tok, mv)
		}
	}
	// Idempotent latch.
	stmts = nil
	if err := EnsureUsageRollup(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(stmts) != 0 {
		t.Fatalf("latch failed, re-ran DDL: %v", stmts)
	}
}

// TestBackfill_BindsCutoffNotInterpolated: the seed inserts from history with the
// cutoff bound as a param, never interpolated.
func TestBackfill_BindsCutoffNotInterpolated(t *testing.T) {
	var last dsCall
	oe, oet := execDatastore, ensureUsageTable
	execDatastore = func(_ context.Context, stmt string, args ...any) error { last = dsCall{stmt, args}; return nil }
	ensureUsageTable = func(context.Context) error { return nil }
	rollupReady.Store(false)
	t.Cleanup(func() { execDatastore, ensureUsageTable = oe, oet; rollupReady.Store(false) })

	if err := BackfillUsageRollup(context.Background(), day(2026, 7, 1)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(last.sql, "INSERT INTO hanzo.usage_rollup_daily") || !strings.Contains(last.sql, "WHERE timestamp < ?") {
		t.Fatalf("backfill DDL wrong: %s", last.sql)
	}
	if len(last.args) != 1 || last.args[0] != "2026-07-01 00:00:00" {
		t.Fatalf("cutoff must be exactly one bound arg: %v", last.args)
	}
}

// TestBackfill_SnapsCutoffToDay: the seed selects the ledger by `timestamp` and the
// guard counts the rollup by `day`, so a mid-day bound would seed a partial day the
// guard cannot see. Both are snapped to UTC midnight.
func TestBackfill_SnapsCutoffToDay(t *testing.T) {
	var last dsCall
	oe, oet := execDatastore, ensureUsageTable
	execDatastore = func(_ context.Context, stmt string, args ...any) error { last = dsCall{stmt, args}; return nil }
	ensureUsageTable = func(context.Context) error { return nil }
	rollupReady.Store(false)
	t.Cleanup(func() { execDatastore, ensureUsageTable = oe, oet; rollupReady.Store(false) })

	noon := day(2026, 7, 28).Add(12*time.Hour + 34*time.Minute)
	if err := BackfillUsageRollup(context.Background(), noon); err != nil {
		t.Fatal(err)
	}
	if last.args[0] != "2026-07-28 00:00:00" {
		t.Fatalf("cutoff must snap to UTC midnight, got %v", last.args[0])
	}
}

// TestBackfill_GuardIsScopedToTheSeedRange pins the defect that made the seed
// unrunnable: EnsureUsageRollup creates the incremental view on the first read, the
// view starts capturing immediately, and a guard that counted the WHOLE table then
// saw rows forever — so every non-forced seed answered 409 and pre-view history was
// never laid down (the live rollup held 2.7% of the ledger). The guard must count
// only the days the seed would write.
func TestBackfill_GuardIsScopedToTheSeedRange(t *testing.T) {
	// A rollup in exactly the state the live view leaves it: rows from the day the
	// view was created onward, nothing before.
	const viewLiveFrom = "2026-07-28"
	f := installFakeDS(t, func(sql string, args []any) []map[string]any {
		if !strings.Contains(sql, "SELECT count() AS n FROM "+rollupTable) {
			return nil
		}
		if len(args) == 1 && args[0].(string) <= viewLiveFrom {
			return []map[string]any{{"n": uint64(0)}} // nothing seeded in that range yet
		}
		return []map[string]any{{"n": uint64(42)}} // unscoped count, or a later cutoff
	})
	app := mountApp(t)
	super := withHeader(principalHeaders("admin", "root"), "X-User-IsAdmin", "true")

	code, body := doJSON(t, app, "POST", "/v1/usage/rollup/backfill?before="+viewLiveFrom+"T00:00:00Z", super, nil)
	if code != 200 {
		t.Fatalf("seeding days the live view never captured must be allowed, got %d: %s", code, body)
	}
	// It asked the range question, not the whole-table one.
	var asked bool
	for _, c := range f.allCalls() {
		if strings.Contains(c.sql, "SELECT count() AS n FROM "+rollupTable) {
			asked = true
			if !strings.Contains(c.sql, "WHERE day < toDate(?)") {
				t.Fatalf("guard must be scoped to the seed range: %s", c.sql)
			}
		}
	}
	if !asked {
		t.Fatal("guard never ran")
	}
	// Re-seeding the same range still refuses — the double-count guard is intact.
	code, _ = doJSON(t, app, "POST", "/v1/usage/rollup/backfill?before=2026-08-01T00:00:00Z", super, nil)
	if code != 409 {
		t.Fatalf("re-seeding covered days must be 409, got %d", code)
	}
}

// TestBackfill_GuardsAgainstDoubleRun: a non-empty rollup refuses re-seeding (which
// would double-count) unless forced — checked at the handler.
func TestBackfill_GuardsAgainstDoubleRun(t *testing.T) {
	// rollup already has rows; execDatastore is a no-op.
	installFakeDS(t, func(sql string, _ []any) []map[string]any {
		if strings.Contains(sql, "SELECT count() AS n FROM "+rollupTable) {
			return []map[string]any{{"n": uint64(42)}}
		}
		return nil
	})
	app := mountApp(t)
	// SuperAdmin, no force → 409 conflict (already seeded).
	code, _ := doJSON(t, app, "POST", "/v1/usage/rollup/backfill", withHeader(principalHeaders("admin", "root"), "X-User-IsAdmin", "true"), nil)
	if code != 409 {
		t.Fatalf("non-forced re-seed must be 409, got %d", code)
	}
	// Non-super → 403 regardless.
	code, _ = doJSON(t, app, "POST", "/v1/usage/rollup/backfill", principalHeaders("acme", "alice"), nil)
	if code != 403 {
		t.Fatalf("non-super backfill must be 403, got %d", code)
	}
}

// TestRollup_CarriesNano: the derived table stores the ledger's money column, so a
// read can sum it and round once. A rollup of cents would round per (org, user,
// model, day) and every read would add those roundings up.
func TestRollup_CarriesNano(t *testing.T) {
	for name, ddl := range map[string]string{"table": rollupTableDDL, "mv": rollupMVDDL, "seed": backfillDDL} {
		if !strings.Contains(ddl, "cost_nano") {
			t.Fatalf("%s does not carry cost_nano: %s", name, ddl)
		}
		if strings.Contains(ddl, "cost_cents") {
			t.Fatalf("%s still stores a rendering: %s", name, ddl)
		}
	}
	// The seed NAMES its target columns. A bare INSERT ... SELECT matches by
	// position, so a column added ahead of the money would silently land in it.
	if !strings.Contains(backfillDDL, "(day, organization, user_id, model, requests,") {
		t.Fatalf("seed must name its target columns: %s", backfillDDL)
	}
}
