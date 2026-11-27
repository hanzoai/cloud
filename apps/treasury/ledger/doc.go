// Package ledger is the native, double-entry accounting core of the Hanzo finance
// stack — the store-agnostic engine that owns EVERY accounting rule (balanced
// postings, a non-negative reserve fund, idempotent journal entries, revenue-share
// math) and NOTHING about how those facts are persisted or served.
//
// It has ZERO coupling to the cloud binary: no zip/HTTP, no cloud.Deps, no IAM,
// no SQLite. Persistence is a PORT — the Store/Tx interfaces below — so the same
// core runs on Hanzo Base (HIP-0105 per-tenant SQLite), an in-memory fake, or any
// future backend by swapping the adapter. This is deliberate: the package is the
// SEED of the native `hanzoai/finance` central ledger (the Go replacement for the
// Formance Ledger/Wallets/Reconciliation stack). Lifting it to that repo is a
// directory move + import-path rewrite — no logic changes — because all the domain
// lives here and every dependency points INWARD (adapters depend on the core, never
// the reverse).
//
// The model is the classic one, reduced to its essence:
//
//   - An ACCOUNT is an id in a chart (e.g. "fund:reserve", "revenue:platform",
//     "payout:referral"). Its BALANCE is the signed sum of every posting to it.
//   - A JOURNAL ENTRY is an atomic set of POSTINGS whose signed amounts sum to
//     EXACTLY zero (double-entry: every debit has an equal credit). An entry that
//     does not balance is refused — the ledger can never be made inconsistent.
//   - Money in minor units (cents), int64. No floats ever touch a balance.
//
// The reserve fund is one account ("fund:reserve"): revenue-share accruals credit
// it, backed payouts debit it, and a debit that would drive it negative is refused.
// That single rule — the fund cannot overdraw — is what makes the growth-loop
// payouts a REAL, backed reserve instead of unbounded minting.
package ledger
