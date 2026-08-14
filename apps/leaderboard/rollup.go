// The datastore rollup (#43): a DERIVED per-day pre-aggregation of hanzo.cloud_usage
// that makes leaderboard + activity reads cheap. It is NOT a second metering path —
// it is a rollup of the ONE ledger, so it can never double-count what cloud_usage
// already records.
//
// ENGINE CHOICE — SummingMergeTree, not AggregatingMergeTree. Every rollup metric
// is a pure SUM (requests, tokens, cost); distinct-user / distinct-model counts fall
// out of the (org,user,model,day) grain at read time. SummingMergeTree is the
// simplest engine that does exactly "collapse rows with the same sort key by summing
// the rest", stores plain integers (read with a normal sum(), no -Merge), and is the
// established house pattern (commerce.daily_sales_mv). AggregatingMergeTree would only
// earn its keep for non-sum aggregate states (uniq/quantile) — we have none.
//
// MONEY. The rollup carries cost_nano, the ledger's own money column, NOT cents. A
// rollup of cents would be a rounding per (org, user, model, day), and the reads add
// those up — so the board would show the sum of the roundings rather than what was
// spent. Cents are derived once, at the read, by datastore.Spend.
//
// INCREMENTAL MV. rollupMV is attached to hanzo.cloud_usage: every INSERT into the
// ledger fires it, pre-aggregating THAT block into the rollup. The MV SELECT is pure
// and TYPE-EXACT (toDate→Date, sum(UInt32)→UInt64, count()→UInt64, all matching the
// target columns), so it cannot fail on a valid ledger row — it never endangers the
// (fire-and-forget) metering write. It captures rows inserted AFTER its creation;
// pre-existing history is seeded ONCE by the deploy-gated backfill.

package leaderboard

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

const (

	// rollupTableDDL is the ONE definition of the derived rollup. PARTITION BY month
	// keeps SummingMergeTree merges (which collapse same-key rows) within a partition;
	// ORDER BY leads with organization so org-scoped reads are index-efficient AND the
	// tenant predicate is the leading key. TTL matches the ledger's 2-year retention.
	rollupTableDDL = `
		CREATE TABLE IF NOT EXISTS hanzo.usage_rollup_daily (
			day Date,
			organization String,
			user_id String,
			model String,
			requests UInt64,
			prompt_tokens UInt64,
			completion_tokens UInt64,
			total_tokens UInt64,
			cost_nano Int64
		) ENGINE = SummingMergeTree()
		PARTITION BY toYYYYMM(day)
		ORDER BY (organization, user_id, model, day)
		TTL day + INTERVAL 2 YEAR`

	// rollupMVDDL attaches the incremental pre-aggregation to the ledger. Every SELECT
	// term is type-exact with the target column (see the engine note above).
	rollupMVDDL = `
		CREATE MATERIALIZED VIEW IF NOT EXISTS hanzo.usage_rollup_daily_mv
		TO hanzo.usage_rollup_daily
		AS SELECT
			toDate(timestamp) AS day,
			organization,
			user_id,
			model,
			toUInt64(count()) AS requests,
			toUInt64(sum(prompt_tokens)) AS prompt_tokens,
			toUInt64(sum(completion_tokens)) AS completion_tokens,
			toUInt64(sum(total_tokens)) AS total_tokens,
			toInt64(sum(cost_nano)) AS cost_nano
		FROM hanzo.cloud_usage
		GROUP BY day, organization, user_id, model`

	// backfillDDL seeds pre-MV history from the ledger. `WHERE timestamp < ?` (the MV
	// creation watermark / a cutoff) avoids double-counting the rows the live MV already
	// captured. Same grain + same type-exact projection as the MV.
	//
	// The target columns are NAMED. A bare INSERT ... SELECT matches by position, so a
	// column added to the rollup ahead of the money would land the money in it.
	backfillDDL = `
		INSERT INTO hanzo.usage_rollup_daily
			(day, organization, user_id, model, requests,
			 prompt_tokens, completion_tokens, total_tokens, cost_nano)
		SELECT
			toDate(timestamp) AS day,
			organization,
			user_id,
			model,
			toUInt64(count()) AS requests,
			toUInt64(sum(prompt_tokens)) AS prompt_tokens,
			toUInt64(sum(completion_tokens)) AS completion_tokens,
			toUInt64(sum(total_tokens)) AS total_tokens,
			toInt64(sum(cost_nano)) AS cost_nano
		FROM hanzo.cloud_usage
		WHERE timestamp < ?
		GROUP BY day, organization, user_id, model`
)

// rollupColumnMigrations bring an ALREADY-CREATED rollup up to the current shape,
// exactly as apps/datastore does for the ledger: CREATE TABLE IF NOT EXISTS is a
// no-op on a table that already exists, so an additive column needs its own
// idempotent ALTER. Additive only — nothing here rewrites or drops, so it is safe
// on every boot.
//
// The MATERIALIZED VIEW is NOT in reach of this list, and that is the thing to know
// about a cluster that already has one. CREATE MATERIALIZED VIEW IF NOT EXISTS is a
// no-op once the view exists, and a view's SELECT cannot be altered, so editing
// rollupMVDDL above changes NOTHING there — the old view keeps filling the old
// column, and every read of the money answers 0. On such a cluster this file is a
// promise until an operator swaps the view by hand, in this order:
//
//	DROP VIEW hanzo.usage_rollup_daily_mv;                              -- stop the old fill
//	TRUNCATE TABLE hanzo.usage_rollup_daily;                            -- derived; rebuilt below
//	ALTER TABLE hanzo.usage_rollup_daily DROP COLUMN IF EXISTS cost_cents;
//	<restart, so EnsureUsageRollup recreates the view from rollupMVDDL>
//	POST /v1/usage/rollup/backfill?before=<today, UTC midnight>         -- re-seed history
//
// It stays a hand step because it stops capture and empties a table, and nothing
// should decide that about itself on a boot. Rows written between the DROP and the
// restart reach the ledger but not the rollup, and the seed only lays down WHOLE
// days, so run it just after 00:00 UTC and that gap is minutes of the current day.
var rollupColumnMigrations = []string{
	`ALTER TABLE hanzo.usage_rollup_daily ADD COLUMN IF NOT EXISTS cost_nano Int64`,
}

var rollupReady atomic.Bool

// EnsureUsageRollup creates the derived rollup table + the incremental MV if they do
// not exist (the base ledger first, since the MV reads it). Idempotent and latched:
// a transient datastore blip does not permanently poison later attempts. Every
// leaderboard/activity read calls it first — the same discipline as
// EnsureCloudUsageTable — so the feature self-provisions its rollup the first time it
// runs against a connected datastore.
func EnsureUsageRollup(ctx context.Context) error {
	if rollupReady.Load() {
		return nil
	}
	if err := ensureUsageTable(ctx); err != nil {
		return fmt.Errorf("ensure cloud_usage: %w", err)
	}
	if err := execDatastore(ctx, rollupTableDDL); err != nil {
		return fmt.Errorf("create rollup table: %w", err)
	}
	for _, stmt := range rollupColumnMigrations {
		if err := execDatastore(ctx, stmt); err != nil {
			return fmt.Errorf("migrate rollup table: %w", err)
		}
	}
	if err := execDatastore(ctx, rollupMVDDL); err != nil {
		return fmt.Errorf("create rollup mv: %w", err)
	}
	rollupReady.Store(true)
	return nil
}

// rollupCutoff normalizes a seed bound to the rollup's own grain: UTC midnight.
// The seed selects LEDGER rows by `timestamp`, the guard counts ROLLUP rows by
// `day`, and only a day-aligned bound makes those two the same set of days. A
// mid-day bound writes a PARTIAL row for that day which the live view may also
// hold, and once both are in a SummingMergeTree no count can separate them.
func rollupCutoff(t time.Time) time.Time { return t.UTC().Truncate(24 * time.Hour) }

// rollupRowsSeeded counts the rollup rows the seed would ADD TO — the days strictly
// before the cutoff. It is the guard against double-counting, and it must be scoped
// to the seed's own range rather than to the whole table.
//
// Scoping it to the whole table is what made the seed unrunnable. EnsureUsageRollup
// creates the incremental view on the FIRST leaderboard read, the view starts
// capturing on the next ledger insert, and the guard then sees a non-empty rollup
// forever — so every non-forced seed answered 409 and pre-view history was never
// laid down. Forcing was the only way through and the code itself says forcing
// doubles. Live cost: the rollup held 530 of the ledger's 19,792 requests (2.7%),
// and every leaderboard and activity read served from it under-reported by 97%.
//
// The bound is one-sided by design: this seeds "everything before the view existed",
// once. Widening the cutoff afterwards is a genuine re-seed of days already covered,
// and it is refused — ?force=true is the deliberate override.
func rollupRowsSeeded(ctx context.Context, before time.Time) (int64, error) {
	rows, err := queryDatastore(ctx,
		"SELECT count() AS n FROM "+rollupTable+" WHERE day < toDate(?)",
		rollupCutoff(before).Format("2006-01-02"))
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return aInt64(rows[0]["n"]), nil
}

// BackfillUsageRollup seeds the rollup from ledger history before the cutoff. It is
// the DEPLOY-GATED, run-ONCE step: because SummingMergeTree accumulates, re-seeding
// a day it already holds would double that day, so the handler guards on the seed's
// own range (rollupRowsSeeded) or an explicit force. `before` is snapped to UTC
// midnight so the seed's day-range and the guard's are identical.
func BackfillUsageRollup(ctx context.Context, before time.Time) error {
	if err := EnsureUsageRollup(ctx); err != nil {
		return err
	}
	return execDatastore(ctx, backfillDDL, rollupCutoff(before).Format("2006-01-02 15:04:05"))
}
