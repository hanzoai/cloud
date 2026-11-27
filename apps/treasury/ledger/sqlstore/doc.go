// Package sqlstore is the Hanzo Base (HIP-0105 per-tenant SQLite) adapter for the
// ledger core: it implements ledger.Store + ledger.Tx over one database, and
// nothing more. It carries the storage concern the core deliberately does not — the
// SAME single-connection + WAL pattern every clients/* store uses (referrals, crm,
// prompts), so it is Base-compatible and drops into the unified binary unchanged.
//
// The database is a SQLite file or, in a deployment on hanzo-sql, a schema on
// PostgreSQL, and the SQL here runs unchanged on both (sdk/dialect states the
// rules; the suite runs on both through internal/sqltest). The one thing the engines do
// not share is how a write transaction excludes another: SQLite's pinned connection
// does it, and on PostgreSQL [Store.Tx] takes the store's write lock.
//
// It imports the core (ledger) and the pool opener — never cloud, zip, or IAM. When the
// core is lifted to hanzoai/finance this adapter travels with it as the default backend.
//
// MONEY IS EXACT AND BIG. Amounts are 18-decimal USD (1e-18, the EVM/uint256 unit) held as
// big.Int money.Amount — a value exceeds a 64-bit INTEGER past ~$9.20, so amount
// columns are TEXT (the signed 18-decimal integer string) and an account's balance is a
// maintained running total (treasury_accounts.balance), NOT a SQL SUM (you cannot SUM a
// decimal-string column, and a busy wallet's million usage postings must not be re-summed
// on every gate read). The running balance is updated inside the same transaction as each
// posting, so it can never drift from the journal. No amount is ever a float or a NUMERIC:
// the string is the same on both engines, byte for byte.
package sqlstore
