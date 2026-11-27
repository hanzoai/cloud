package explorer

import (
	"context"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
)

// state is explorer's own data: the chain-data upstream client. The shared deps live
// in the embedded cloud.Base — brand (s.Brand) is the chain family surfaced and env
// (s.Env) is the network tier reported as an indexer's `network`.
type state struct {
	cl *client
}

// Mount wires the chain-data surface onto app per HIP-0106.
func Use(app cloud.Router, deps cloud.Deps) error {
	return cloud.Use(app, deps, "explorer", build, routes)
}

// build dials the chain-data upstreams (INDEXER_URL / GRAPH_URL from env) and
// records the informative mount line.
func build(b cloud.Base) (state, error) {
	st := state{cl: newClient()}
	b.Log.Info("explorer chain-data surface mounted",
		"indexer", st.cl.indexer, "graph", st.cl.graph, "brand", b.Brand, "env", b.Env)
	return st, nil
}

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// routes is the ONE place the surface is wired.
func routes(app cloud.Router, s *cloud.Service[state]) {
	// A typed op receives only a context, so the validated org and the request the
	// caller's own Authorization is forwarded from are parked there by
	// cloud.Bridge. This subsystem does not install it: the program's composer
	// does, once at the root, after the identity check that mints the org and
	// before any subsystem registers a route — an order only the composer can
	// hold.

	// TYPED ops. explorer owns two top-level nouns rather than one prefix, so each is
	// declared at its whole path on the app's registry — the identity every
	// projection (document, MCP tool, CLI command, SDK method) keys on.
	o := ops{s: s}
	g := app.Group("/v1/explorer")
	g.Get("/indexers", o.listIndexers)
	g.Get("/oracles", o.listOracles)
}

// ops is the receiver the chain-data ops hang off. A method value is the only
// bound form cmd/zipdoc can lift prose from, so ops are methods and not closures.
type ops struct{ s *cloud.Service[state] }

// gate enforces the ONE tenancy boundary that applies to public chain data: a
// validated IAM principal MUST be present (principal.Acting, the typed-op reader of
// the org cloud.Bridge parked), so an unauthenticated caller reads nothing. The org
// itself is not a filter key here (a ledger is public within a brand); it is the
// proof-of-auth gate, checked in ONE place before any handler touches an upstream.
func gate(ctx context.Context) error {
	_, err := principal.Acting(ctx)
	return err
}

// forwarded is the caller's own Authorization, which the upstream read passes
// through when no service token is configured (client.go's authorize). It is the
// caller's credential and not an addressing value, so it is read off the request
// rather than modeled as an In field a caller could also put in a body. Empty off
// the HTTP path, where there is no request and therefore no identity to forward.
func forwarded(ctx context.Context) string {
	c, ok := cloud.Request(ctx)
	if !ok {
		return ""
	}
	return c.Header("Authorization")
}

// ---- indexers (luxfi/indexer explorer REST) ----

// indexersOut is the answer of the indexer list: the deployment's chain indexer(s).
type indexersOut struct {
	// Indexers is one row per reachable chain indexer, or an empty list when the
	// indexer is unreachable — never a fabricated row.
	Indexers []indexerView `json:"indexers"`
}

// ListIndexers reports the deployment's chain indexer(s) and how far each has
// indexed. Identity and health come from the indexer's /health; the latest indexed
// block (height + time) from its /v1/explorer/blocks. The row EXISTS if EITHER call
// reaches the indexer; when the indexer is entirely unreachable the answer degrades
// to an honest-EMPTY list at 200, not a 502. No chain HEAD is exposed by the indexer
// REST, so `lag` is honestly omitted rather than fabricated.
func (o ops) listIndexers(ctx context.Context, _ *cloud.Unit) (*indexersOut, error) {
	if err := gate(ctx); err != nil {
		return nil, err
	}
	s := o.s
	auth := forwarded(ctx)
	health, hErr := s.State.cl.health(ctx, auth)
	block, bErr := s.State.cl.latestBlock(ctx, auth)
	if hErr != nil && bErr != nil {
		// Indexer unreachable in any form — degrade to an honest-EMPTY list (200), not a
		// 502 that surfaces as a console error on the page for every org without a chain
		// indexer deployed. Same graceful fold as visor/clusters; an empty list is honest
		// (no indexers), never a fabricated row.
		s.Log.Warn("indexer unreachable; returning empty indexer list", "err", bErr)
		return &indexersOut{Indexers: []indexerView{}}, nil
	}
	return &indexersOut{Indexers: []indexerView{toIndexerView(health, block, s.Brand, s.Env)}}, nil
}

// ---- oracles (luxfi/graph priceFeeds) ----

// oraclesOut is the answer of the oracle list: the on-chain price/data feeds.
type oraclesOut struct {
	// Oracles is one row per on-chain price feed, or an empty list when the graph is
	// unreachable or carries none — never a fabricated feed.
	Oracles []oracleView `json:"oracles"`
}

// ListOracles reports the on-chain price/data oracles from the graph's O-Chain
// PriceFeed registry. A reachable graph with no feeds answers an honest empty list;
// an unreachable or erroring graph likewise degrades to an empty list at 200 rather
// than a 502, so the console never error-toasts. No feed is ever fabricated.
func (o ops) listOracles(ctx context.Context, _ *cloud.Unit) (*oraclesOut, error) {
	if err := gate(ctx); err != nil {
		return nil, err
	}
	s := o.s
	feeds, err := s.State.cl.priceFeeds(ctx, forwarded(ctx))
	if err != nil {
		// Price-feed oracle unreachable — honest-EMPTY (200), not a 502 page error.
		s.Log.Warn("oracle price feeds unreachable; returning empty oracle list", "err", err)
		return &oraclesOut{Oracles: []oracleView{}}, nil
	}
	out := make([]oracleView, 0, len(feeds))
	for _, f := range feeds {
		out = append(out, toOracleView(f))
	}
	return &oraclesOut{Oracles: out}, nil
}
