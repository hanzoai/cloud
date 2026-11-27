// Package finance is the prepaid wallet your org pays from: deposits in, usage
// debits out, always balanced.
//
// It is a per-CUSTOMER, double-entry balance on the native ledger core
// (apps/treasury/ledger, the same engine the platform reserve posts to). It
// registers NO routes and NO ops — it is the in-process implementation of
// cloud's money.FinanceClient (package alias finance.Client, mirroring commerce.Client),
// the ONE money client the ai prepaid gate, the admin grant, commerce's credit mint and
// the edge meter all bill through; billing is the customer-facing endpoint onto it.
//
// ONE LIGHTWEIGHT FILE PER ORG. Each org's books are an isolated Hanzo Base (SQLite)
// file at <dataDir>/orgs/<org>/finance.db (a separate <...>/finance-test.db for sandbox
// money, so test and live never mix). There is NO shared or relational database — the
// file IS the tenant boundary, so one org's wallet writes can never appear in another
// org's read. A file opens on first use and is cached; opens are serialized so a
// concurrent first touch opens exactly once.
//
// MONEY-SAFETY. Every write is a BALANCED double-entry posting inside the org's own
// file: a deposit is funding → wallet (credit the customer, debit the source); a usage
// debit is wallet → revenue:platform (debit the customer, credit platform revenue) — so
// every customer debit IS a platform-revenue credit in one atomic entry, and a file's
// postings always sum to zero.
//
// TWO SOURCES, TWO ACCOUNTS (cash.go). Money a settled payment brought in is CASH:
// funding:cash → cash[:<user>]. Money Hanzo minted — a grant, a promo, a carry — is
// CREDIT: funding:platform → wallet[:<user>]. The balance is their sum; a debit draws
// cash first. Which account a posting names IS the record of where the money came
// from, so nothing reads a tag or a memo to know it. Both writes are idempotent on their ref
// (the ledger's (kind,program,ref) idempotency): a usage debit on UsageInput.Ref and a
// deposit on DepositInput.Ref, so a retried debit or a fixed-ref backfill posts AT MOST
// ONCE; an empty Ref takes a fresh, server-minted one and stays additive (grants stack,
// metered calls each bill). A usage ref is unique WITHIN THE WALLET it debits, so one
// subject's key can never swallow another's, and a ref that already names a posting of a
// DIFFERENT amount is refused rather than answered with the first one's entry — same key
// is not same act.
// Amounts are int64 minor units (USD cents) — no float ever touches a balance. A balance
// read is the settled ledger balance, clamped at zero. A payment agreed before it is made
// sets its amount aside in a hold (hold.go), which the balance no longer counts.
package finance
