// Package usage is what your org ran and what it cost, broken down per account.
//
// It serves /v1/usage over one window grammar, and absorbed the account-usage board
// from apps/links, which owns links and nothing usage. It is NOT the only usage
// address — billing serves the wallet's own /v1/billing/usage{,/accounts} — and the
// two answer different questions: this one composes the categorized roll-up, billing
// reports the raw drain on the wallet.
//
// The surface:
//
//   - POST /v1/usage          record account-usage samples (the collector's data
//     plane; write path over the hanzo.account_usage warehouse series, datastore.go).
//   - GET  /v1/usage/samples  one provider account's own lane dash (the time series).
//   - GET  /v1/usage/summary  the flagship own-scoped footprint roll-up (below).
//   - GET  /v1/usage/analytics{,/access}  the entitlement-gated rich per-org read.
//
// The summary answers "what am I running and what does it cost" by composing THREE
// complementary sources, each degrading independently to honest zeros (a source
// marker says which answered — a partial deploy never fabricates spend or usage):
//
//   - Spend (the genuinely-missing categorized cost roll-up): the commerce ledger —
//     usage/rollup (authoritative month-to-date consumed + prepaid wallet) plus the
//     raw transaction ledger, rolled up server-side into spend-by-category over
//     time. Every metered resource (GPU, machine-hours, LLM tokens, datastore
//     footprint) debits this ONE ledger with a category tag, so it is already the
//     unified cost source — this endpoint is the categorizing lens over it.
//   - LLM usage totals: hanzo.cloud_usage, the per-org warehouse ledger (the same
//     table /v1/event/* and the o11y board read). Totals only here — the
//     per-model / timeseries detail stays at /v1/event/*. They carry the prompt
//     cache's reads, writes, share and saving, and beside them the org's Kai
//     decisions (the rows filed under model kai).
//   - The account board: the caller's OWN linked provider accounts (a Claude Max
//     plan's window %, metered from the provider's own login) beside the org's
//     Hanzo-routed usage, every row labelled by source/scope and NEVER summed. This
//     is the account-usage global view, unified here from apps/links.
//
// The console Usage view composes THIS with the existing org-scoped inventory
// endpoints (/v1/compute/machines, /v1/compute/gpus, /v1/agent, provisioning lists) for the
// per-kind counts — one screen, one categorized cost lens.
//
// TENANT ISOLATION (the bar). The org is the VALIDATED IAM owner claim (principal.Org
// — the trusted X-Org-Id the identity middleware minted from the caller's verified
// bearer, HIP-0026; NEVER a client header) AND a validated principal is required
// (c.User() set only for a verified bearer). The commerce subject is pinned
// server-side to that org; every warehouse query binds org (and, for the account
// board, subject) POSITIONALLY. A caller can only ever read/write its OWN org —
// fail-closed: no principal → 401.
//
// Its plugin does not declare OwnsHealth, so the host's generic GET /v1/usage/health
// liveness route stands — a distinct path that never shadows these. The manifest.Apps
// sequence is what binds /v1/usage/* ahead of the ai subsystem's /v1/* catch-all.
package usage
