// Package websearch exposes Hanzo-native Web Search + Scrape on the unified
// cloud-api /v1 plane, so hanzo.chat's web_search agent tool runs entirely on
// Hanzo infrastructure with NO external SaaS provider, per HIP-0106.
//
// hanzo.chat (LibreChat fork) implements web_search as a fixed 3-stage pipeline
// whose provider contracts are frozen by the upstream client
// (@librechat/agents tools/search). The only self-hostable, key-less-to-a-SaaS
// providers it accepts are:
//   - search provider  "searxng"   → GET  {searxngInstanceUrl}/search?q=&format=json
//     ← {results:[{url,title,content,img_src?}]}
//   - scraper provider "firecrawl" → POST {firecrawlApiUrl}/{version}/scrape
//     body {url,formats} ← {success,data:{markdown,metadata}}
//     (reranker is optional; we omit it — provider+scraper is sufficient.)
//
// This subsystem serves BOTH contracts under /v1/websearch, backed by Hanzo's
// own services — never a third-party search API:
//   - GET  /v1/websearch/search        SearXNG-shaped. Served NATIVELY in-process
//     by a keyless Go meta-search (search.go) — no SearXNG pod, no search SaaS.
//   - POST /v1/websearch/v1/scrape      Firecrawl-shaped. Served NATIVELY in-process
//     (also /v1/websearch/scrape)       by clients/crawl — fetch, extract, render —
//     returning {success,data:{markdown,metadata}}.
//
// Both halves are now in-process Go, for the same reason and by the same shape: a
// keyless meta-search here, a fetch-and-extract in clients/crawl. Neither has a pod
// to be down. Scrape previously dialled a separate crawler at crawl.hanzo.svc that
// did NOT exist — the name was NXDOMAIN — so this surface answered 200 while every
// scrape inside it returned success:false. clients/crawl is the same capability
// with no network hop and no second deployment to keep alive.
//
// The chat server calls these SERVER-SIDE in-cluster, so point
// searxngInstanceUrl / firecrawlApiUrl at this surface (public api.hanzo.ai/v1
// or the internal cloud-api svc DNS — same binary either way).
//
// AUTH: two callers, two ONE-WAY-equivalent gates, never an open proxy —
//   - SEARCH (/v1/websearch/search) admits EITHER a validated principal
//     (principal.Validated — X-User-Id minted by the identity middleware from a
//     verified JWT: the signed-in console user via the /cloud bearer proxy) OR the
//     shared service key WEBSEARCH_API_KEY as X-API-Key (the hanzo.chat server,
//     which reaches cloud service-to-service with no user principal). A caller with
//     neither is refused.
//   - SCRAPE (/v1/websearch/*/scrape) requires the shared key as a Bearer (the chat
//     server path only; the console surfaces scrape read-only, does not drive it).
//
// An unset key 503s and any missing/mismatched key 401s on the key path; a request
// with a validated principal never needs the key. So neither surface is ever an
// open proxy, and the signed-in console user reaches search without the shared key.
package websearch

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/crawl"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// apiKey is the shared service key the chat server presents (firecrawl Bearer /
// searxng X-API-Key). KMS-sourced, synced as WEBSEARCH_API_KEY.
func apiKey() string { return strings.TrimSpace(os.Getenv("WEBSEARCH_API_KEY")) }

// ── SearXNG-shaped search: native keyless meta-search, in-process ────────────

// searchQuery is the SearXNG /search request: the two parameters this surface
// honours. The client also sends format=json, which is the only format served
// and so is neither read nor declared.
type searchQuery struct {
	// Q is the search terms. An empty q returns an empty result set.
	Q string `json:"q"`
	// Language is a BCP-47-ish hint (en, en-US, de) passed to the engines; empty
	// leaves the engine's own default.
	Language string `json:"language"`
}

// search runs the enabled keyless engines concurrently and merges their results,
// deduped by normalized URL and capped, in the exact SearXNG /search?format=json
// envelope — so a SearXNG client decodes it verbatim. It runs in-process: there
// is no SearXNG pod and no third-party search API. An engine that fails or is
// bot-challenged contributes nothing and the request still answers.
//
// Example: {"q": "post-quantum signatures", "language": "en"}
// Response: {"query": "post-quantum signatures", "number_of_results": 1, "results": [{"url": "https://example.com/page", "title": "Example", "content": "…", "engine": "bing"}]}
func search(ctx context.Context, in *searchQuery) (*searchResponse, error) {
	out := metaSearch(ctx, strings.TrimSpace(in.Q), strings.TrimSpace(in.Language))
	return &out, nil
}

// searchNative is the same answer for the methods other than GET. SearXNG serves
// /search over more than one verb and the reply is built from the query string
// either way, so this runs the SAME core rather than a second implementation —
// the typed GET above is what every projection reads.
func searchNative(c *zip.Ctx) error {
	out, err := search(c.Context(), &searchQuery{Q: c.Query("q"), Language: c.Query("language")})
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, out)
}

// errBody is the refusal shape both gates have always written: the status
// repeated in the body next to the reason.
type errBody struct {
	Status int    `json:"status"`
	Error  string `json:"error"`
}

// admitSearch admits a caller two ONE-WAY-equivalent ways, so /v1/websearch/search
// is never an open proxy to the Hanzo-operated metasearch (a request-forgery +
// cost surface): a VALIDATED PRINCIPAL (the signed-in console user, already
// authenticated and metered) OR the shared service key on X-API-Key (the
// hanzo.chat server, which reaches cloud with no user principal).
//   - key unset          → 503 (surface not configured; never "open to all").
//   - X-API-Key missing   → 401 (constant-time compare of "" vs want fails).
//   - X-API-Key mismatch  → 401.
//
// The LibreChat searxng client sends the configured searxngApiKey as X-API-Key
// (universe chat configmap wires searxngApiKey=${WEBSEARCH_API_KEY}), so the
// real caller is unaffected; only anonymous callers are turned away.
func admitSearch(c *zip.Ctx) error {
	if principal.Validated(c) {
		return c.Next()
	}
	want := apiKey()
	if want == "" {
		return c.JSON(http.StatusServiceUnavailable, errBody{http.StatusServiceUnavailable, "web search not configured"})
	}
	got := strings.TrimSpace(c.Header("X-API-Key"))
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return c.JSON(http.StatusUnauthorized, errBody{http.StatusUnauthorized, "invalid api key"})
	}
	return c.Next()
}

// admitScrape requires the shared key as a Bearer — the chat-server path only,
// which is what firecrawl always sends. Fail-closed: an unset key 503s rather
// than defaulting open.
func admitScrape(c *zip.Ctx) error {
	want := apiKey()
	if want == "" {
		return c.JSON(http.StatusServiceUnavailable, errBody{http.StatusServiceUnavailable, "web search not configured"})
	}
	got := strings.TrimSpace(strings.TrimPrefix(c.Header("Authorization"), "Bearer "))
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return c.JSON(http.StatusUnauthorized, errBody{http.StatusUnauthorized, "invalid api key"})
	}
	return c.Next()
}

// ── Firecrawl scrape: adapt Hanzo Crawl → the firecrawl response shape ──────

// firecrawlRequest is the subset of the firecrawl /scrape body we honor.
type firecrawlRequest struct {
	// URL is the page to fetch. An empty url is answered with success:false, not
	// with an HTTP error — see scrape.
	URL string `json:"url"`
}

// firecrawlResponse is the exact shape the LibreChat firecrawl client decodes:
// {success, data:{markdown, metadata}}.
type firecrawlResponse struct {
	// Success is false when the page could not be read; the status is 200 either
	// way, because the firecrawl client treats a non-2xx as a broken provider and
	// can disable the tool.
	Success bool `json:"success"`
	// Data is the extracted page, present only on success.
	Data *firecrawlData `json:"data,omitempty"`
	// Error says why the scrape failed, verbatim.
	Error string `json:"error,omitempty"`
}

type firecrawlData struct {
	// Markdown is the readable content of the page.
	Markdown string `json:"markdown"`
	// Metadata is what the page declared about itself (og:*, description, …).
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// scrape fetches ONE url and returns it as markdown in the firecrawl envelope,
// served in-process by the native crawler — there is no crawler pod and no
// third-party scrape API. The page lands in the SAME corpus /v1/crawl fills, so
// one crawl and one archive whichever door was used. A url that cannot be read is
// a 200 with success:false and the reason: the firecrawl client treats a non-2xx
// as a broken provider and can disable the tool.
//
// Example: {"url": "https://hanzo.ai/about"}
// Response: {"success": true, "data": {"markdown": "# About Hanzo\n…", "metadata": {"title": "About Hanzo"}}}
func scrape(ctx context.Context, in *firecrawlRequest) (*firecrawlResponse, error) {
	if in.URL == "" {
		return &firecrawlResponse{Success: false, Error: "missing url"}, nil
	}
	page, err := crawl.Read(ctx, scrapeScope(ctx), in.URL)
	if err != nil {
		return &firecrawlResponse{Success: false, Error: err.Error()}, nil
	}
	return &firecrawlResponse{
		Success: true,
		Data:    &firecrawlData{Markdown: page.Markdown, Metadata: page.Metadata},
	}, nil
}

// scrapeScope reads the corpus scope from the VERIFIED principal, never from the
// body — a caller who could name the prefix could name another tenant's. The chat
// server holds the shared key and no user principal, so its pages land in the
// shared prefix, which is the honest home for a page fetched on nobody's behalf.
func scrapeScope(ctx context.Context) crawl.Scope {
	c, ok := cloud.Request(ctx)
	if !ok {
		return crawl.Scope{}
	}
	org, _ := principal.Org(c)
	return crawl.Scope{Org: org, Project: principal.Project(c)}
}

// Mount registers the web-search surface on app.
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("websearch.Mount: nil app")
	}
	logger := deps.Logger
	if logger == nil {
		return fmt.Errorf("websearch.Mount: nil deps.Logger")
	}
	logger = logger.New("subsystem", "websearch")

	// /v1/websearch/search admits a caller two ONE-WAY-equivalent ways, checked at
	// the zip layer so the same request either reaches native meta-search or is
	// refused — it is NEVER an open surface (F2):
	//   1. a VALIDATED PRINCIPAL — principal.Validated(c) is true when the identity
	//      middleware set X-User-Id from a verified JWT (the SAME gate the whole
	//      /v1 data plane uses). This is the console user surface: the /cloud proxy
	//      mints a short-lived user bearer, cloud validates it, and search runs
	//      (no shared key needed, the caller is already authenticated + metered).
	//   2. the shared X-API-Key — searchGuard, for the hanzo.chat server which reaches
	//      cloud WITHOUT a user principal (service-to-service). 503 when the key is
	//      unset, 401 on a missing/wrong key.
	// A caller with NEITHER a validated principal NOR a valid key is refused (401/503),
	// so the anonymous-forge / open-surface path stays closed.
	zapp := cloud.ZipApp(app)
	if zapp == nil {
		return fmt.Errorf("websearch.Mount: router is not backed by a *zip.App; typed ops have nowhere to register")
	}

	g := app.Group("/v1/websearch")
	// The typed-op bridge FIRST — fiber runs middleware in registration order, so
	// one installed after the leaves would never run, and scrape reads its corpus
	// scope off the request the bridge parks. Serve installs one app-wide too;
	// nesting is harmless, and this is what makes the surface testable on a bare app.
	g.Use(cloud.Bridge())
	// The two gates are DIFFERENT and each is bounded to the paths it guards:
	// search takes a principal OR the key, scrape takes the key only. They are
	// middleware because a refusal is not the shape a typed op answers with — a
	// caller turned away here never reaches the search or the crawl.
	app.Group("/v1/websearch/search").Use(admitSearch)
	app.Group("/v1/websearch/scrape").Use(admitScrape)
	app.Group("/v1/websearch/v1/scrape").Use(admitScrape)

	// GET is the typed op — the registration every projection (the document, the
	// MCP tool list, the CLI) reads. It is registered BEFORE the catch-all below,
	// which fiber resolves in registration order, so a GET lands here.
	zip.Get(zapp, "/v1/websearch/search", search)
	// …and the other methods keep answering as they always have, through the same
	// core. SearXNG serves /search over more than one verb; removing them would
	// 405 a caller that works today.
	g.All("/search", searchNative)

	// Firecrawl builds {apiUrl}/{version}/scrape; pin firecrawlVersion:v1 so the
	// client POSTs /v1/websearch/v1/scrape. Also accept the bare /scrape. Both are
	// the same operation at two addresses, so both are declared: a document that
	// named only one would leave the address the client actually builds undescribed.
	zip.Post(zapp, "/v1/websearch/v1/scrape", scrape, zip.WithOperationID("websearchScrapeV1"))
	zip.Post(zapp, "/v1/websearch/scrape", scrape, zip.WithOperationID("websearchScrape"))

	logger.Info("web search surface mounted (native searxng-compat meta-search + firecrawl-compat scrape, both in-process)")
	return nil
}
