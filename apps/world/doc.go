// Package world is a live news feed filtered to what your project cares about.
//
// A per-org, per-project intelligence feed that normalizes GDELT +
// host-allowlisted RSS/Atom into one NewsItem stream, applies the project's
// keyword/region/source filter, and serves it over REST + SSE. It is the Go
// backend for the World monitor frontend (hanzoai/world), replacing that app's
// Vercel edge functions (api/gdelt-doc.js, api/rss-proxy.js) with an
// org-scoped, in-binary subsystem.
//
// Surface (/v1 only; org/project-scoped except where marked public):
//
//	GET /v1/world            index: the product and its wires         -> {…} PUBLIC
//	GET /v1/world/news       merged, filtered, freshest-first feed -> {items:[…]}
//	GET /v1/world/pipeline   per-project pipeline config (read)    -> {…}
//	PUT /v1/world/pipeline   per-project pipeline config (write)   -> {…}
//	GET /v1/world/limits     a World plan's rate/alert/model gates -> {…} PUBLIC
//	GET /v1/world/stream     SSE live refresh (ZAP-native)         -> event: news
//
// Two more wires answer under this prefix and are NOT served by this binary:
// /v1/world/mcp (Model Context Protocol) and /v1/world/zap (ZAP over WebSocket)
// are carved off the cloud catch-all by the ingress and answered by world-gw. The
// generated document cannot declare them — openapi.Describe renders prose only
// for a route this router actually serves, which is what stops the document
// claiming an operation nothing answers — so GET /v1/world names them instead.
// That op is the only place in the product's own surface those addresses appear.
//
// TENANT ISOLATION is enforced SERVER-SIDE on every request. The (org, project)
// tuple is principal.Org + principal.Project (the values SanitizeIdentity minted
// from the VALIDATED bearer, HIP-0026) — never a query param, body, or client
// header. Every store statement carries `WHERE org=? AND project=?`; the SSE bus
// filters on org and the stream loop drops other projects. A request with no
// validated principal is a 403.
//
// SECURITY. The RSS fetcher is an SSRF boundary: a feed URL's host must be in the
// ported rss-proxy.js allowlist (allowlist.go), enforced at BOTH the PUT write
// boundary and at fetch time, including on redirect targets (client CheckRedirect).
//
// The host routes by first matching prefix in manifest.Apps order, and world's row
// (/v1/world) precedes the ai row that answers the bare /v1 remainder.
package world
