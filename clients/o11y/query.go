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
// upstream SigNoz engine version is an internal impl detail resolved HERE, never
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

	"github.com/zap-proto/zip"
)

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
