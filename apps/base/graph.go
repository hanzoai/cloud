// Copyright (C) 2020-2026, Hanzo AI Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package base

import (
	"context"
	"net/http"

	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

func init() {
	spec.Describe(apiPrefix+"/graphql", http.MethodGet,
		"Read your org Base's GraphQL schema",
		"The schema of the caller's org Base as GraphQL SDL: a type per collection with its "+
			"fields, relations and the records pointing at it, a filter input per collection, "+
			"orderBy, connections, aggregates and the writes. It is that org's own, so it changes "+
			"with its collections. /v1/graphql carries the same schema under `base`.")
	spec.Describe(apiPrefix+"/graphql", http.MethodPost,
		"Query or write your org Base with GraphQL",
		"Runs one GraphQL request against the caller's org Base. Every field is answered by "+
			"the Base's own record, aggregate and history routes as the caller, so what a field "+
			"reads or writes is what the same request over /v1/base/collections may. No "+
			"subscriptions: realtime is /v1/base/realtime.")
}

// The org's Base as typed GraphQL, at /v1/base/graphql: GET is the schema of
// that org's own collections, POST runs a request against it on zip's engine.
// Every field is answered by Base's graph package with a request on the org
// Base's own router, carrying the caller's request, so a field reaches exactly
// what the same request reaches over /v1/base/collections and nothing here
// decides who may read or write what. The host composes this graph into
// /v1/graphql as `base`.

// graphGate refuses what has no org Base to answer: a caller acting in no org
// (401 or 403, the words the rest of /v1/base uses), or a deployment that embeds
// no Bases (503).
func graphGate(c *zip.Ctx) error {
	if _, ok := principal.Org(c); !ok {
		return principal.Refused(c)
	}
	if s := mounted; s == nil || s.pool == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "base: hosted Bases are not embedded in this deployment")
	}
	return c.Next()
}

// orgGraph pins the caller's org Base for the length of the request: the
// fields of a GraphQL answer are asked after the root resolver returns, and an
// evicted Base would close its database under them. zip cancels ctx when the
// request is done, which is when the pin is released.
func orgGraph(ctx context.Context) (interface {
	Schema(context.Context) (string, error)
	Resolve(context.Context, string, zip.Selection) (any, error)
}, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	s := mounted
	if s == nil || s.pool == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "base: hosted Bases are not embedded in this deployment")
	}
	g, release, err := s.pool.pinGraph(org)
	if err != nil {
		return nil, err
	}
	context.AfterFunc(ctx, release)
	return g, nil
}

func orgSchema(ctx context.Context) (string, error) {
	g, err := orgGraph(ctx)
	if err != nil {
		return "", err
	}
	return g.Schema(ctx)
}

func orgResolve(ctx context.Context, kind string, s zip.Selection) (any, error) {
	g, err := orgGraph(ctx)
	if err != nil {
		return nil, err
	}
	return g.Resolve(ctx, kind, s)
}
