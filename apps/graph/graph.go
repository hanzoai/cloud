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
// HONEST FAILURE. Absent a reachable upstream the handler degrades to an honest-EMPTY
// list (200) — the same graceful fold as visor/clusters, NOT a 502 that surfaces as a
// console error for every org without an indexer/graph deployed. A reachable-but-empty
// upstream likewise returns an empty list — it NEVER fabricates an indexer or oracle row.
package graph

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// state is graph's own data: the chain-data upstream client. The shared deps live
// in the embedded cloud.Base — brand (s.Brand) is the chain family surfaced and env
// (s.Env) is the network tier reported as an indexer's `network`.
type state struct {
	cl *client
}

// Mount wires the chain-data surface onto app per HIP-0106.
func Mount(app cloud.Router, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "graph", build, routes)
}

// build dials the chain-data upstreams (INDEXER_URL / GRAPH_URL from env) and
// records the informative mount line.
func build(b cloud.Base) (state, error) {
	st := state{cl: newClient()}
	b.Log.Info("graph chain-data surface mounted",
		"indexer", st.cl.indexer, "graph", st.cl.graph, "brand", b.Brand, "env", b.Env)
	return st, nil
}

// routes is the ONE place the surface is wired. Both reads are zip TYPED ops, so
// the REST route, the OpenAPI document, the MCP tool and the CLI command all come
// from the one declaration. The bridge goes on FIRST — fiber runs middleware in
// registration order — because a typed op is handed only a context, so the request
// (and the validated principal it proves) is parked there.
func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	z := cloud.ZipApp(app)
	app.Group("/v1/indexers").Use(cloud.Bridge())
	app.Group("/v1/oracles").Use(cloud.Bridge())

	zip.Get(z, "/v1/indexers", o.listIndexers, zip.WithOperationID("listIndexers"))
	zip.Get(z, "/v1/oracles", o.listOracles, zip.WithOperationID("listOracles"))
}

// ops binds the service to graph's typed handlers. A TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so it
// arrives as a RECEIVER and every op is a method value. That is also the only bound
// form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

// gate enforces the ONE tenancy boundary that applies to public chain data: a
// validated IAM principal MUST be present (principal.OrgFrom), so an unauthenticated
// caller reads nothing. The org itself is not a filter key here (a ledger is public
// within a brand); it is the proof-of-auth gate, checked in ONE place before any
// handler touches an upstream. It returns the request the upstream client needs to
// forward the caller's own Authorization — absent off the HTTP path, where the
// honest answer is that there is no caller, so the op refuses.
func gate(ctx context.Context) (*zip.Ctx, error) {
	if _, ok := principal.OrgFrom(ctx); !ok {
		return nil, zip.ErrForbidden("X-Org-Id required")
	}
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, zip.ErrForbidden("X-Org-Id required")
	}
	return c, nil
}

// ---- indexers (luxfi/indexer explorer REST) ----

// IndexerList is the chain-indexer roster the console's Indexer page renders.
type IndexerList struct {
	// Indexers is one row per chain indexer this deployment is wired to; empty
	// when no indexer is reachable, never a fabricated row.
	Indexers []Indexer `json:"indexers"`
}

// listIndexers reports the deployment's chain indexer(s) and how far each has indexed.
//
// Identity and health come from the indexer's /health; the latest indexed block
// (height and time) from /v1/explorer/blocks.
// The row EXISTS if EITHER call reaches the indexer; when the indexer is entirely
// unreachable it degrades to an honest-EMPTY list (200), not a console-error 502.
// No chain HEAD is exposed by the indexer REST, so `lag` is honestly omitted
// rather than fabricated.
//
// Response: {"indexers": [{"id": "Lux C-Chain", "chain": "Lux C-Chain", "network": "mainnet", "height": "12345", "status": "active", "updatedAt": "2024-04-01T00:00:00Z"}]}
func (o ops) listIndexers(ctx context.Context, _ *struct{}) (*IndexerList, error) {
	c, err := gate(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	health, hErr := s.State.cl.health(c)
	block, bErr := s.State.cl.latestBlock(c)
	if hErr != nil && bErr != nil {
		// Indexer unreachable in any form — degrade to an honest-EMPTY list (200), not a
		// 502 that surfaces as a console error on the page for every org without a chain
		// indexer deployed. Same graceful fold as visor/clusters; an empty list is honest
		// (no indexers), never a fabricated row.
		s.Log.Warn("indexer unreachable; returning empty indexer list", "err", bErr)
		return &IndexerList{Indexers: []Indexer{}}, nil
	}
	return &IndexerList{Indexers: []Indexer{toIndexerView(health, block, s.Brand, s.Env)}}, nil
}

// ---- oracles (luxfi/graph priceFeeds) ----

// OracleList is the on-chain oracle roster the console's Oracles page renders.
type OracleList struct {
	// Oracles is one row per on-chain price feed; empty when the graph is
	// unreachable or carries no feeds, never a fabricated feed.
	Oracles []Oracle `json:"oracles"`
}

// listOracles reports the on-chain price and data oracles the graph indexes.
//
// The source is luxfi/graph's O-Chain PriceFeed registry — a REAL registry, the
// graph's own oracle resolver.
// A reachable graph with no feeds returns an honest empty list; an unreachable or
// erroring graph likewise degrades to an honest-EMPTY list (200), not a 502.
// No feed is ever fabricated.
//
// Response: {"oracles": [{"id": "LUX-USD", "name": "LUX/USD", "feed": "LUX/USD", "value": "2.50", "source": "O-Chain", "status": "active", "updatedAt": "2024-04-01T00:00:00Z"}]}
func (o ops) listOracles(ctx context.Context, _ *struct{}) (*OracleList, error) {
	c, err := gate(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	feeds, err := s.State.cl.priceFeeds(c)
	if err != nil {
		// Price-feed oracle unreachable — honest-EMPTY (200), not a 502 page error.
		s.Log.Warn("oracle price feeds unreachable; returning empty oracle list", "err", err)
		return &OracleList{Oracles: []Oracle{}}, nil
	}
	out := make([]Oracle, 0, len(feeds))
	for _, f := range feeds {
		out = append(out, toOracleView(f))
	}
	return &OracleList{Oracles: out}, nil
}
