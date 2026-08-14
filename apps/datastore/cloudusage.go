package datastore

import (
	"context"
	"fmt"
	"sync/atomic"
)

// hanzo.cloud_usage is the per-inference spend ledger: the ai router appends one
// row per call, and cloud's read surfaces (analytics, usage, evals, leaderboard,
// link) aggregate it. A fresh warehouse has no such table, so every reader must
// create it idempotently before its first SELECT.
//
// The DDL below is a SECOND copy of ai/object/cloud_usage.go's, and that is
// deliberate. ai keeps its own for its WRITE path; this one serves cloud's read
// path. The reason cloud cannot just call ai's: aiobject.EnsureCloudUsageTable
// execs through object.DatastoreExec, whose connection is opened only by
// object.InitDatastore, which runs only inside aimod.Mount. None of
// cmd/{analytics,ask,evals,leaderboard,link,rollingcap,usage} link the ai module,
// so that call ALWAYS returned "datastore: not connected" and every caller
// silently took its failure branch — an honest-empty dashboard on a warehouse
// that was up. Meanwhile THIS package's connection is live in exactly those
// binaries.
//
// The alternative to a copy is a func var the host injects. That seam is never
// wired here by construction: the whole point of these binaries is that they do
// not link ai, so the var is nil in all seven and the read path is dead again.
// Two copies of idempotent DDL against one table converge; a nil hook does not.
// Keep in lockstep with ai/object/cloud_usage.go and the zapWriteUsage INSERT.
const cloudUsageTableDDL = `
	CREATE TABLE IF NOT EXISTS hanzo.cloud_usage (
		id String,
		timestamp DateTime,
		owner String,
		user_id String,
		organization String,
		project String,
		model String,
		provider String,
		request_id String,
		prompt_tokens UInt32,
		completion_tokens UInt32,
		total_tokens UInt32,
		cache_read_tokens UInt32,
		cache_write_tokens UInt32,
		cost_cents UInt64,
		currency String,
		status String,
		error_msg String,
		is_premium UInt8,
		is_stream UInt8,
		client_ip String,
		byo UInt8,
		fee_cents Int64,
		account String,
		cost_nano Int64,
		billed_nano Int64,
		margin_nano Int64,
		unpriced UInt8
	) ENGINE = ReplacingMergeTree()
	ORDER BY (timestamp, organization, user_id, id)
	TTL timestamp + INTERVAL 2 YEAR`

// cloudUsageColumnMigrations bring an ALREADY-EXISTING table up to the current
// schema: CREATE TABLE IF NOT EXISTS is a no-op on a table created before these
// columns were added, so each additive column also needs an idempotent
// ADD COLUMN IF NOT EXISTS. Applied after the CREATE so both a fresh and a legacy
// table converge on the same shape.
var cloudUsageColumnMigrations = []string{
	`ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS byo UInt8`,
	`ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS fee_cents Int64`,
	`ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS account String`,
	// Nano-USD margin ledger (money-of-record): cost_nano = provider COGS,
	// billed_nano = org debit, margin_nano = billed_nano − cost_nano. cost_cents
	// is that ONE row rendered in cents; spend over a SET of rows comes from
	// cost_nano through Spend, never from adding the renderings.
	`ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS cost_nano Int64`,
	`ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS billed_nano Int64`,
	`ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS margin_nano Int64`,
	// unpriced = 1 when the model had no configured price and billed at the default,
	// so the honest "priced?" flag is queryable in the warehouse, not just the span.
	`ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS unpriced UInt8`,
	// project is the caller's org SUB-SCOPE (X-Project-Id); it lets the per-org
	// metrics board narrow WITHIN an org by project. Additive: a pre-existing row
	// carries '' (the org's default project == whole-org view).
	`ALTER TABLE hanzo.cloud_usage ADD COLUMN IF NOT EXISTS project String`,
}

// Spend is the spend of a SET of usage rows in whole US cents, and the ONE place
// that answer is written. Every board that reports money — analytics, the admin
// ledger, evals, per-org usage, the leaderboard rollup — selects this, so they
// cannot disagree about what a window cost.
//
// It sums the money first and rounds ONCE, half a cent up. The order is the whole
// point. cost_nano is the money of record and a served call routinely costs a
// fraction of a cent, so rounding each row and adding the roundings charges a whole
// cent for a call that cost a tenth of one: an error that grows with the number of
// calls rather than with the money, which is why it reads worst exactly where
// traffic is cheapest and highest. Summed first, the same rows round to what they
// cost, and the most a window can be off by is half a cent.
//
// The rounding is integer, not round(x/1e7): ClickHouse's round() is float and
// banker's at the midpoint, and this must match the writer's nanoToCents.
//
// It reads the ledger's cost_nano and the rollup's, which carry the same name, so
// one expression serves both.
const Spend = "intDiv(sum(cost_nano) + 5000000, 10000000)"

// createDatabase makes the target database idempotently, the same first step
// clients/sbom takes. ai's version omits it because ai's write path only ever
// runs where the ai router already created the database; cloud's read path has no
// such guarantee — on a fresh warehouse these readers ARE the first writer, and a
// CREATE TABLE against a database that does not exist fails.
const createDatabase = `CREATE DATABASE IF NOT EXISTS hanzo`

var cloudUsageReady atomic.Bool

// EnsureCloudUsage creates hanzo.cloud_usage if absent, then applies the additive
// column migrations so a pre-existing table gains the newer columns. Only SUCCESS
// latches, so a warehouse still connecting at boot is retried on the next call
// rather than poisoned forever.
func EnsureCloudUsage(ctx context.Context) error {
	if cloudUsageReady.Load() {
		return nil
	}
	if !Ready() {
		return fmt.Errorf("datastore not connected")
	}
	if err := Exec(ctx, createDatabase); err != nil {
		return fmt.Errorf("ensure database: %w", err)
	}
	if err := Exec(ctx, cloudUsageTableDDL); err != nil {
		return err
	}
	for _, stmt := range cloudUsageColumnMigrations {
		if err := Exec(ctx, stmt); err != nil {
			return err
		}
	}
	cloudUsageReady.Store(true)
	return nil
}
