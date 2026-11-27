// Package books is double-entry finance and accounting: chart of accounts, ledger, bank
// reconciliation, and the reports that prove the ledger balances.
//
// It serves /v1/finance: a fixed chart of accounts, an append-only general ledger,
// bank feeds with reconciliation, receipt scanning, and the trial / P&L /
// position reports.
//
// WHY THIS EXISTS. finance holds the money (a prepaid wallet: deposits + usage debits);
// billing PROJECTS that wallet for the customer UI. Neither keeps BOOKS — a general
// ledger with a chart of accounts, revenue recognition, and a trial balance. This domain
// ports ERPNext's Accounts SEMANTICS (process_gl_map: merge → toggle → round-off → the
// debit==credit invariant) to Go, with ZERO of its Python/Postgres.
//
// IT RECORDS MONEY, IT NEVER MOVES IT. Three sources post: commerce
// GET /v1/billing/transactions (ingest.go), a read-only bank connector (bank.go), and a
// reviewed receipt scan (scan.go). Every one of them lands through the SAME post() choke
// point, and none of them can mint a deposit, credit, or payout — this domain only READS
// money that already moved and writes the accounting twin, so the books can restate but
// never create money.
//
// TENANT ISOLATION. Every read resolves the caller's OWN org from the validated
// principal (principal.Org — the gateway-minted X-Org-Id, HIP-0026), and each org's
// books live in a physically separate {DataDir}/orgs/{slug}/books.db (sandbox →
// books-sandbox.db). One org can never read another's ledger.
package books
