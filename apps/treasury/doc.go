// Package treasury is the reserve fund behind every payout: real capital, held and
// accounted for.
//
// It is the platform's OWN fund — a backed pool that stands behind the growth-loop
// payouts (referrals, affiliates, OSS authors), so a payout is a debit against funded
// capital and never unbounded minting. Where finance tracks what each CUSTOMER holds
// and spends, treasury tracks what the PLATFORM holds.
//
// It answers under its own name: GET /v1/treasury{,/accounts} for the tenant and the
// SuperAdmin /v1/admin/treasury/* reserve mutations (HIP-0139 §3.2 — the operator's
// view of <name>, served by <name>). The full table is below.
//
// It is the cloud-facing adapter around the ledger-of-record PORT (ledger.Backend):
// this file owns HTTP, tenant scoping, audit and the KMS-signed L1 anchor + the Hanzo
// policy/fund/payout logic; the backend owns the double-entry. Two backends satisfy
// the port — the native Base/SQLite engine (apps/treasury/ledger, offline/default,
// and the SAME engine apps/finance keeps each org's prepaid wallet in) and the Formance
// adapter (apps/treasury/formance, the Postgres-backed ledger of record when
// FORMANCE_LEDGER_URL is wired). Selecting one is a config flip.
//
// Storage tiers (OLTP → OLAP): the authoritative double-entry is the ledger-of-record
// backend (single-writer, overdraw-guarded — reserve/revenue/house on the house
// tenant). Cross-tenant GLOBAL analytics is the datastore OLAP projection in the
// shared hanzoai/datastore, fed by the SAME event stream o11y already emits — treasury
// money-actions mirror there via cloud's audit datastore mirror, so there is NO second
// metering pipeline. datastore is NEVER the ledger of record; single-tenant drill-down
// reads the authoritative ledger, cross-tenant aggregates read the projection.
//
// ONE scope-aware engine, three tenancy surfaces — the tenant is derived
// from the validated IAM identity, house/reserve is locked to SuperAdmin, and a
// per-org caller only ever sees its own tenant:
//
//	GET  /v1/treasury                  (org)          reserve health + policy (the pool backing MY payouts)
//	GET  /v1/treasury/accounts         (org)          MY ledger accounts (admin: ?scope=house | ?org=<t>)
//	GET  /v1/admin/treasury            (SuperAdmin) full report + journal + anchor status
//	POST /v1/admin/treasury/policy     (SuperAdmin) set the revenue-share %
//	POST /v1/admin/treasury/sweep      (SuperAdmin) accrue the revenue-share into the fund for a period
//	POST /v1/admin/treasury/seed       (SuperAdmin) inject bootstrap capital into the fund
//	POST /v1/admin/treasury/anchor        (SuperAdmin) anchor the ledger root on Hanzo L1
//	PUT  /v1/admin/treasury/anchor/signer (SuperAdmin) the reserve MPC wallet that signs anchors
//
// The three surfaces (admin.hanzo.ai SuperAdmin, console.hanzo.ai per-org customer,
// finance.hanzo.ai per-org operator) are the SAME engine projected by IAM scope. A
// separate frontend agent builds the console + finance surfaces against this contract.
// The reserve-fund admin board is the `treasury` admin head (distinct from the
// existing /v1/admin/finance COGS/margin god-view in apps/admin — they compose,
// never collide).
//
// serve.go auto-registers GET /v1/treasury/health.
package treasury
