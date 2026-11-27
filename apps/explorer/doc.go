// Package explorer is chain data: your block indexers and how far each has caught
// up, plus the on-chain price feeds.
//
// It serves them at /v1/explorer/indexers and /v1/explorer/oracles — read from
// the Lux chain-data plane, principal-gated, and never fabricated.
//
// The two upstreams are luxfi/indexer (the per-network block/event indexer,
// explorer REST at /v1/explorer/*) and luxfi/graph (the GraphQL query layer that
// indexes O-Chain oracle price feeds). It exists so the console's Indexer and
// Oracles pages read REAL chain state from ONE place (api.hanzo.ai/v1/*) instead
// of rendering "not connected".
//
// This subsystem OWNS no chain state — the indexer and graph do. It is a thin,
// principal-gated translator: it reads the indexer's health + latest block and the
// graph's priceFeeds, and re-shapes them into the exact JSON the console modules
// consume (types.go). It never fabricates: an indexer row is a real indexer's real
// chain + indexed height, an oracle row is a real on-chain price feed, and telemetry
// the upstream does not carry (the chain HEAD, hence true indexing lag) is honestly
// omitted (renders "—"), NEVER invented.
//
// Surface (every route gated by the validated principal; HIP-0026):
//
//	GET /v1/explorer/indexers  the chain indexer(s) + status -> {indexers:[indexerView]}
//	GET /v1/explorer/oracles   on-chain price feeds (graph)  -> {oracles:[oracleView]}
//
// Indexers maps to the indexer's per-network indexing status (chain, network, height,
// health); Oracles maps to luxfi/graph's O-Chain PriceFeed registry — the two chain-
// data concepts the two console pages need.
//
// ISOLATION. Chain data is a PUBLIC ledger, but scoped per BRAND: each brand's cloud
// is wired to its OWN indexer/graph (INDEXER_URL / GRAPH_URL), so the surfaced
// networks are always the caller's brand's — exactly as the console's Networks proxy
// scopes networks per brand. Within a brand a ledger is public, so there is no per-org
// private row to leak; the ONE tenancy boundary that applies is principal-gating:
// every route requires a validated IAM principal (principal.Acting → 401 without
// one), so an unauthenticated caller reads nothing.
//
// HONEST FAILURE. Absent a reachable upstream the handler degrades to an honest-EMPTY
// list (200) — the same graceful fold as visor/clusters, NOT a 502 that surfaces as a
// console error for every org without an indexer/graph deployed. A reachable-but-empty
// upstream likewise returns an empty list — it NEVER fabricates an indexer or oracle row.
package explorer
