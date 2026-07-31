// Copyright 2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

// Flat builder query — the ONE canonical public path for the o11y builder query:
//
//	POST /v1/o11y/query        (instant builder query)
//	POST /v1/o11y/query_range  (range/list builder query — the console's composite list)
//
// The public surface is FLAT and version-less: one /v1/, no nested /api/vN. The
// upstream engine version is an internal impl detail resolved HERE, never
// leaked into the public route.
//
// Why a cloud-side route instead of the module's version-less alias: the hanzoai/o11y
// wildcard maps /v1/o11y/<resource> onto the VERSION-LESS alias /api/<resource>, which
// AddVersionlessAliases resolves to the HIGHEST engine version (v5). But the console's
// composite list payload is v3-shaped (`compositeQuery.{queryType,builderQueries}` →
// `data.result[].list`); the v5 composite accepts only `{queries:[…]}` and 400s the v3
// shape (`unknown field "queryType"`). So the flat public path must resolve to the v3
// engine handler specifically — a request+response pair that stays consistent. This
// route pins that mapping SERVER-SIDE (flat in → /api/v3/<resource> out), so the client
// speaks ONLY the flat path and never an engine version.
//
// Registered by mountScope (order 69) BEFORE the order-70 wildcard, so Fiber's in-order
// match binds this POST ahead of the proxy — the same rule the scoped reads use.

package o11y

import (
	"net/http"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// Both builder routes are reverse proxies — request body, query string, upstream
// status, headers and body all ride through untouched — so there is no Go type
// for "whatever the runtime answered" and they cannot be typed ops
// (typed_wire_test.go's untypedByDesign). zipdoc has nothing to lift; the prose is
// declared here, beside the path pin that is the whole reason the route exists.
func init() {
	openapi.Describe("/v1/o11y/query", http.MethodPost,
		"Run one builder query against the caller's telemetry",
		"Runs the console's composite builder query and answers the engine's own response "+
			"untouched — body, status and headers ride through both ways, because the shape "+
			"here is the query engine's, not this layer's.\n\n"+
			"This flat path is the ONE canonical public address for the builder query, and "+
			"pinning it server-side is the point: the version-less alias resolves to the "+
			"engine's highest version, which rejects the v3-shaped composite payload the "+
			"console speaks. So the engine version is resolved INSIDE the handler and the "+
			"client never names one — a caller that spells a version into the path is coupling "+
			"itself to an internal detail that is free to move.\n\n"+
			"Requires a validated principal, and the tenant is the principal's own org, pinned "+
			"server-side from the validated claim; there is no org selector in the payload or "+
			"the query string that could widen it. Before the runtime is initialized this "+
			"answers 503 rather than an empty result.")
	openapi.Describe("/v1/o11y/query_range", http.MethodPost,
		"Run one ranged builder query against the caller's telemetry",
		"Runs the console's composite builder query over a time range — the list and series "+
			"the trace, log and metric explorers render — and answers the engine's own "+
			"response untouched, body, status and headers alike.\n\n"+
			"Same pin as the instant form and for the same reason: the flat path resolves to a "+
			"specific engine version INSIDE the handler, because the version-less alias "+
			"resolves to one that rejects the composite payload the console sends. The client "+
			"speaks only this path.\n\n"+
			"Requires a validated principal, and the tenant is that principal's own org, pinned "+
			"server-side; nothing in the request can widen it. Before the runtime is "+
			"initialized this answers 503.")
}

// builderQueryHandler returns the flat builder-query route handler for `resource`
// ("query" or "query_range"). It delegates to the SAME gated runtime handler the
// order-70 wildcard uses (runtimeHandler, o11y.go), after pinning the internal path to
// the v3 engine route. runtimeHandler carries the principal gate, so a bearer-less
// request is refused exactly as on every other /v1/o11y read; the composite payload +
// query string ride through unchanged, so the request/response pair is byte-for-byte
// what the v3 handler already serves at the leaked `/v1/o11y/api/v3/<resource>` form.
func builderQueryHandler(resource string) zip.Handler {
	internal := "/api/v3/" + resource
	return zip.AdaptNetHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := runtimeHandler
		if h == nil {
			http.Error(w, "o11y runtime not initialized", http.StatusServiceUnavailable)
			return
		}
		// Resolve the flat public path to the v3 engine route INTERNALLY. The embedded
		// runtime's StripPrefix (ExternalPath=/v1/o11y) finds nothing to strip on an
		// /api/-rooted path and passes it straight to the v3 mux route (the same result
		// the wildcard's rewriteExternalPath produces for the leaked api/v3 form).
		r.URL.Path = internal
		r.URL.RawPath = ""
		h.ServeHTTP(w, r)
	}))
}
