package leaderboard

import (
	"context"
	"strings"
	"testing"
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
	if len(stmts) != 2 {
		t.Fatalf("want table+mv (2 DDL), got %d: %v", len(stmts), stmts)
	}
	if !strings.Contains(stmts[0], "CREATE TABLE IF NOT EXISTS hanzo.usage_rollup_daily") || !strings.Contains(stmts[0], "SummingMergeTree") {
		t.Fatalf("target DDL wrong: %s", stmts[0])
	}
	if !strings.Contains(stmts[1], "CREATE MATERIALIZED VIEW IF NOT EXISTS hanzo.usage_rollup_daily_mv") {
		t.Fatalf("mv DDL wrong: %s", stmts[1])
	}
	// Type-exact projection so the MV can never fail a valid ledger insert.
	for _, tok := range []string{"toDate(timestamp) AS day", "toUInt64(count()) AS requests", "toUInt64(sum(total_tokens))", "GROUP BY day, organization, user_id, model"} {
		if !strings.Contains(stmts[1], tok) {
			t.Fatalf("mv missing type-exact term %q: %s", tok, stmts[1])
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
