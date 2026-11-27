// Package formance is the Formance Ledger adapter for the treasury: it satisfies
// ledger.Backend by posting the reserve fund's double-entry through a live Formance
// Ledger service (Postgres-backed, the production ledger of record) over its v2 HTTP
// API. Formance guarantees the double-entry + the overdraw guard — a transaction
// whose source (@fund:reserve) is underfunded is refused by Numscript source
// semantics (400 INSUFFICIENT_FUND), which this adapter maps to "not backed". We do
// NOT reimplement double-entry here; we drive Formance's.
//
// Integration shape (b) of the finance-stack plan: Formance Ledger runs as its own
// all-Go service against the prod Postgres (hanzo-sql); the unified cloud binary
// CLIENTS to it. Selecting it is one env var (FORMANCE_LEDGER_URL); absent it the
// treasury uses the native engine, so the reserve fund works offline and Formance is
// a drop-in upgrade to the ledger of record.
//
//	FORMANCE_LEDGER_URL     base URL of the Formance Ledger service (e.g.
//	                        http://ledger.finance.svc:8080)
//	FORMANCE_LEDGER_NAME    the ledger name to post into (default "hanzo-treasury")
//	FORMANCE_LEDGER_TOKEN   optional bearer token (client-credentials) for auth
//
// The Hanzo revenue-share POLICY is not a Formance concept (it is Hanzo config, not
// accounting), so it is held in the injected ledger.PolicyStore (the native Base
// store) regardless of which backend owns the journal.
package formance
