// Package web3 is the chain-access surface: which chains this deployment can
// reach, a JSON-RPC endpoint onto each, and the two token reads every wallet UI
// needs.
//
// It replaces the api/ half of hanzoai/bootnode, which was 102 Python files of
// 2018-era Quart serving, in the end, four routes — /, /health, /metrics and
// /v1/fleets/lux/overview. The Go port that already existed there (api-go/) had
// a far richer router than the thing actually running. This is that router,
// finished, in the one binary.
//
// WHAT IT DELIBERATELY DOES NOT DO. bootnode's router also mounted /auth,
// /projects, /api-keys, /wallets and /webhooks. Every one of those is already a
// subsystem here — iam, projects, wallets, webhooks — and re-serving them under
// a second prefix would be two implementations of one noun, which is the exact
// sprawl this move exists to end. web3 owns only what nothing else does:
//
//	GET  /v1/web3/chains          the chains this deployment can reach
//	GET  /v1/web3/chains/:chain   one chain, with liveness
//	POST /v1/web3/rpc/:chain      JSON-RPC, proxied to that chain
//	GET  /v1/web3/tokens/:chain/:address    native + ERC-20 balances for an address
//
// NFTs are not here. They need an indexer to answer at all — ownership and
// metadata are not one eth_call — and explorer already owns the indexer
// relationship. A route that returned an empty list forever would look like a
// feature and be a lie.
//
// THE REGISTRY IS DECLARED, NEVER GUESSED. Chains come from WEB3_CHAINS, a JSON
// object of id -> {name, chainId, rpc}. A deployment with none configured
// serves an empty list and refuses /v1/web3/rpc with 404 — it does not fall back to
// a public endpoint, because a silent fallback means someone's traffic quietly
// leaves the estate.
//
// ISOLATION. A chain is a public ledger, so there is no per-org row to leak, and
// the boundary that applies is the same one explorer uses: every route requires
// a validated principal, so an unauthenticated caller reads nothing and cannot
// use this as an open RPC relay.
package web3
