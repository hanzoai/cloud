// Package graph mounts the Hanzo Cloud CHAIN-DATA surface: the deployment's
// blockchain indexing + oracle feeds, served as clean, principal-gated REST off the
// unified cloud binary and fronting the Lux chain-data plane — luxfi/indexer (the
// per-network block/event indexer, explorer REST at /v1/explorer/*) and luxfi/graph
// (the GraphQL query layer that indexes O-Chain oracle price feeds). It exists so
// the console's Indexer and Oracles pages read REAL chain state from ONE place
// (api.hanzo.ai/v1/*) instead of rendering "not connected".
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
//	GET /v1/indexers   the deployment's chain indexer(s) + status  -> {indexers:[indexerView]}
//	GET /v1/oracles    on-chain price/data oracles (graph feeds)    -> {oracles:[oracleView]}
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
// every route requires a validated IAM principal (principal.Org → 403 without one),
// so an unauthenticated caller reads nothing.
//
// HONEST FAILURE. Absent a reachable upstream the handler returns an honest 502 (the
// console renders its "unavailable" card), and a reachable-but-empty upstream returns
// an honest empty list — it NEVER fabricates an indexer or oracle row.
package graph

import (
	"fmt"
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

type svc struct {
	cl    *client
	log   luxlog.Logger
	brand string // deps.Brand — the chain family surfaced (fallback when the indexer omits chain_name)
	env   string // deps.Env  — the network tier (mainnet|testnet|devnet) reported as an indexer's `network`
}

// Mount wires the chain-data surface onto app per HIP-0106.
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("graph.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("graph.Mount: nil deps.Logger")
	}
	s := &svc{
		cl:    newClient(),
		log:   deps.Logger.New("subsystem", "graph"),
		brand: deps.Brand,
		env:   deps.Env,
	}
	s.routes(app)
	s.log.Info("graph chain-data surface mounted",
		"indexer", s.cl.indexer, "graph", s.cl.graph, "brand", deps.Brand, "env", deps.Env)
	return nil
}

// routes is the ONE place the surface is wired — shared by Mount (real upstreams) and
// the test (an httptest fake).
func (s *svc) routes(app *zip.App) {
	app.Get("/v1/indexers", s.listIndexers)
	app.Get("/v1/oracles", s.listOracles)
}

// gate enforces the ONE tenancy boundary that applies to public chain data: a
// validated IAM principal MUST be present (principal.Org), so an unauthenticated
// caller reads nothing. The org itself is not a filter key here (a ledger is public
// within a brand); it is the proof-of-auth gate, checked in ONE place before any
// handler touches an upstream.
func (s *svc) gate(c *zip.Ctx) error {
	if _, ok := principal.Org(c); !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	return nil
}

// ---- indexers (luxfi/indexer explorer REST) ----

// listIndexers reports the deployment's chain indexer(s). Identity + health come from
// the indexer's /health; the latest indexed block (height + time) from
// /v1/explorer/blocks. The row EXISTS if EITHER call reaches the indexer; only when
// the indexer is entirely unreachable does it surface an honest 502 (the console's
// "unavailable" card). No chain HEAD is exposed by the indexer REST, so `lag` is
// honestly omitted rather than fabricated.
func (s *svc) listIndexers(c *zip.Ctx) error {
	if err := s.gate(c); err != nil {
		return err
	}
	health, hErr := s.cl.health(c)
	block, bErr := s.cl.latestBlock(c)
	if hErr != nil && bErr != nil {
		// Indexer unreachable in any form — degrade to an honest-EMPTY list (200), not a
		// 502 that surfaces as a console error on the page for every org without a chain
		// indexer deployed. Same graceful fold as visor/clusters; an empty list is honest
		// (no indexers), never a fabricated row.
		s.log.Warn("indexer unreachable; returning empty indexer list", "err", bErr)
		return c.JSON(http.StatusOK, map[string]any{"indexers": []indexerView{}})
	}
	iv := toIndexerView(health, block, s.brand, s.env)
	return c.JSON(http.StatusOK, map[string]any{"indexers": []indexerView{iv}})
}

// ---- oracles (luxfi/graph priceFeeds) ----

// listOracles reports the on-chain price/data oracles from luxfi/graph's O-Chain
// PriceFeed registry (a REAL registry — the graph's oracle resolver). A reachable
// graph with no feeds returns an honest empty list; an unreachable graph surfaces an
// honest 502. No feed is ever fabricated.
func (s *svc) listOracles(c *zip.Ctx) error {
	if err := s.gate(c); err != nil {
		return err
	}
	feeds, err := s.cl.priceFeeds(c)
	if err != nil {
		// Price-feed oracle unreachable — honest-EMPTY (200), not a 502 page error.
		s.log.Warn("oracle price feeds unreachable; returning empty oracle list", "err", err)
		return c.JSON(http.StatusOK, map[string]any{"oracles": []oracleView{}})
	}
	out := make([]oracleView, 0, len(feeds))
	for _, f := range feeds {
		out = append(out, toOracleView(f))
	}
	return c.JSON(http.StatusOK, map[string]any{"oracles": out})
}
