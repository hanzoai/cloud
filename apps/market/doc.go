// Package market is what trades on each chain, and how far the read of it got.
//
// The exchange interface draws pools, tokens and a token's history. None of that has
// an on-chain source: the precompiles answer about now, nothing remembers a pool's
// volume, and nothing enumerates pools cheaply — so every figure comes from an
// indexer that ingested the chain, and an indexer is a second system that can be
// absent, down, or up and unwilling. This subsystem asks the registry which chains
// exist, asks each chain's indexer what is on it, and answers with BOTH the figures
// and how far the read got.
//
// THAT SECOND HALF IS THE POINT. A chain with no automated market maker deployed —
// Hanzo, Pars, Osage — has an indexer that answers `{"data":{"factories":[]}}` at
// 200. That is a true sentence about those chains. An indexer that never answered is
// a different sentence, and a client that receives an empty list for both writes the
// same screen for a quiet venue and a broken one. Every answer here therefore
// carries a Reach — read, unconfigured, unreachable, refused — in the interface's own
// vocabulary, so nothing has to guess which silence it is holding. See reach.go.
//
// The surface:
//
//	GET /v1/market/chains            every chain, with its AMM and what it amounts to
//	GET /v1/market/pools?chain=      the pools on one chain
//	GET /v1/market/tokens?chain=     the tokens on one chain
//	GET /v1/market/token?chain=&at=  one token's daily history
//	GET /v1/market/survey?chain=     which settlement precompiles that chain carries
//
// IT READS, AND THAT IS ALL IT CAN DO. There is no write face, no address that
// accepts anything signed, and no store — so this software holds no order, and there
// is nothing here for one to rest in. It does not read the `dex` subgraph, whose root
// fields include `orders`: a list aggregating open offers is an excluded activity,
// and the way not to build one is not to fetch it. It publishes no route, no ranking
// and no recommendation; a pool's two price fields are the ratio its own reserves
// stand at, passed through as the indexer computed them, and this package neither
// derives a mark from them nor puts them in an order.
//
// IT IS NOT A FORWARDER. The exchange had one of those once — an endpoint duplicated
// per brand and per network that reshuffled bytes between a caller and an upstream —
// and it does not come back. What earns an operation here is composing an answer and
// naming it: `chains` reads the registry and both of every chain's figures in one
// call, where a client doing it itself makes one registry request and two more per
// chain, and each row carries its own reach so that one indexer being down describes
// one chain instead of blanking the roster.
package market
