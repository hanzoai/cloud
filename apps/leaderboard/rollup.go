// The datastore rollup (#43): a DERIVED per-day pre-aggregation of hanzo.cloud_usage
// that makes leaderboard + activity reads cheap. It is NOT a second metering path —
// it is a rollup of the ONE ledger, so it can never double-count what cloud_usage
// already records.
//
// ENGINE CHOICE — SummingMergeTree, not AggregatingMergeTree. Every rollup metric
// is a pure SUM (requests, tokens, cost); distinct-user / distinct-model counts fall
// out of the (org,user,model,day) grain at read time. SummingMergeTree is the
// simplest engine that does exactly "collapse rows with the same sort key by summing
// the rest", stores plain UInt64 (read with a normal sum(), no -Merge), and is the
// established house pattern (commerce.daily_sales_mv). AggregatingMergeTree would only
// earn its keep for non-sum aggregate states (uniq/quantile) — we have none.
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
	rollupMV = "hanzo.usage_rollup_daily_mv"

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
			cost_cents UInt64
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
			toUInt64(sum(cost_cents)) AS cost_cents
		FROM hanzo.cloud_usage
		GROUP BY day, organization, user_id, model`

	// backfillDDL seeds pre-MV history from the ledger. `WHERE timestamp < ?` (the MV
	// creation watermark / a cutoff) avoids double-counting the rows the live MV already
	// captured. Same grain + same type-exact projection as the MV.
	backfillDDL = `
		INSERT INTO hanzo.usage_rollup_daily
		SELECT
			toDate(timestamp) AS day,
			organization,
			user_id,
			model,
			toUInt64(count()) AS requests,
			toUInt64(sum(prompt_tokens)) AS prompt_tokens,
			toUInt64(sum(completion_tokens)) AS completion_tokens,
			toUInt64(sum(total_tokens)) AS total_tokens,
			toUInt64(sum(cost_cents)) AS cost_cents
		FROM hanzo.cloud_usage
		WHERE timestamp < ?
		GROUP BY day, organization, user_id, model`
)

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
	if err := execDatastore(ctx, rollupMVDDL); err != nil {
		return fmt.Errorf("create rollup mv: %w", err)
	}
	rollupReady.Store(true)
	return nil
}

// rollupRowCount returns the number of rows currently in the rollup — the guard the
// backfill uses to refuse an accidental double-run.
func rollupRowCount(ctx context.Context) (int64, error) {
	rows, err := queryDatastore(ctx, "SELECT count() AS n FROM "+rollupTable)
	if err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return aInt64(rows[0]["n"]), nil
}

// BackfillUsageRollup seeds the rollup from ledger history with timestamp < before.
// It is the DEPLOY-GATED, run-ONCE step: because SummingMergeTree accumulates, a
// second unguarded run would double a day, so the handler guards on an empty rollup
// (or an explicit force). `before` should be the MV-creation instant (or now) so the
// seed and the live MV do not overlap.
func BackfillUsageRollup(ctx context.Context, before time.Time) error {
	if err := EnsureUsageRollup(ctx); err != nil {
		return err
	}
	return execDatastore(ctx, backfillDDL, before.UTC().Format("2006-01-02 15:04:05"))
}
