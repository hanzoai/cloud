# Finishing native web-search (search.go) — replaces the SearXNG proxy on /v1/websearch/search

search.go is a COMPLETE native meta-search data layer (metaSearch concurrent fan-out +
dedupe, Bing/DDG HTML parsers, engine registry) but is NOT yet wired or verified. To finish:

1. HTTP handler: metaSearch(ctx, q, lang) searchResponse is unserved. Add a handler that
   reads q/lang/format, applies the existing dual auth (principal.Validated OR X-API-Key via
   searchGuard), calls metaSearch, returns searchResponse JSON. Register it in Mount() in
   place of newSearchProxy(searchUpstream()).
2. Tests: no search_test.go exists. Add parser unit tests (fixture HTML), a metaSearch
   merge/dedupe/concurrency test via WEBSEARCH_BING_URL / WEBSEARCH_DDG_URL → httptest server,
   and handler auth tests.
3. Rewrite the tracked websearch_test.go proxy suite (TestMountRoutesThroughRouter,
   TestSearchProxyRewritesToSearchPath, TestSearch*Key*, TestNewSearchProxyRejectsBadURL,
   TestUpstreamDefaultsAndOverrides) — native search ignores WEBSEARCH_UPSTREAM. Remove the
   dead proxy code (newSearchProxy, searchUpstream, defaultSearchUpstream).
4. THE REAL RISK: parsers depend on live Bing/DDG HTML that datacenter egress gets
   bot-challenged on (DDG serves the anomaly page → file defaults to bing-only). A
   self-authored fixture only proves the parser parses the fixture, not real Bing. Verify
   against real upstream from a non-datacenter path before flipping the live hanzo.chat
   web_search path, or it silently returns zero results (green tests, broken prod).
5. golang.org/x/net is already an indirect dep — wiring adds no download, just `go mod tidy`.
