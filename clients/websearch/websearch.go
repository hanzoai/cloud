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

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/crawl"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// apiKey is the shared service key the chat server presents (firecrawl Bearer /
// searxng X-API-Key). KMS-sourced, synced as WEBSEARCH_API_KEY.
func apiKey() string { return strings.TrimSpace(os.Getenv("WEBSEARCH_API_KEY")) }

// ── SearXNG-shaped search: native keyless meta-search, in-process ────────────
// search.go's metaSearch runs the enabled keyless engines and returns the exact
// SearXNG /search?format=json envelope, so the LibreChat searxng client decodes
// it verbatim — no SearXNG pod, no third-party search API. This replaces the
// retired reverse proxy. Reads the SearXNG query params (q, language).
func searchNative(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	lang := strings.TrimSpace(r.URL.Query().Get("language"))
	writeJSON(w, http.StatusOK, metaSearch(r.Context(), q, lang))
}

// searchGuard REQUIRES the shared service key, fail-closed exactly like the
// scrape sibling — /v1/websearch/search must not be an open proxy to the
// Hanzo-operated metasearch instance (a request-forgery + cost surface).
//   - key unset          → 503 (surface not configured; never "open to all").
//   - X-API-Key missing   → 401 (constant-time compare of "" vs want fails).
//   - X-API-Key mismatch  → 401.
//
// The LibreChat searxng client sends the configured searxngApiKey as X-API-Key
// (universe chat configmap wires searxngApiKey=${WEBSEARCH_API_KEY}), so the
// real caller is unaffected; only anonymous callers are turned away.
func searchGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := apiKey()
		if want == "" {
			writeErr(w, http.StatusServiceUnavailable, "web search not configured")
			return
		}
		got := strings.TrimSpace(r.Header.Get("X-API-Key"))
		if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			writeErr(w, http.StatusUnauthorized, "invalid api key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ── Firecrawl scrape: adapt Hanzo Crawl → the firecrawl response shape ──────

// firecrawlRequest is the subset of the firecrawl /scrape body we honor.
type firecrawlRequest struct {
	URL string `json:"url"`
}

// firecrawlResponse is the exact shape the LibreChat firecrawl client decodes:
// {success, data:{markdown, metadata}}.
type firecrawlResponse struct {
	Success bool           `json:"success"`
	Data    *firecrawlData `json:"data,omitempty"`
	Error   string         `json:"error,omitempty"`
}

type firecrawlData struct {
	Markdown string                 `json:"markdown"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

func scrapeHandler(w http.ResponseWriter, r *http.Request) {
	// Bearer auth (firecrawl always sends Authorization: Bearer <key>); fail
	// closed if unconfigured.
	want := apiKey()
	if want == "" {
		writeErr(w, http.StatusServiceUnavailable, "web search not configured")
		return
	}
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		writeErr(w, http.StatusUnauthorized, "invalid api key")
		return
	}

	var req firecrawlRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.URL == "" {
		writeJSON(w, http.StatusOK, firecrawlResponse{Success: false, Error: "missing url"})
		return
	}

	page, err := crawl.Fetch(r.Context(), req.URL)
	if err != nil {
		writeJSON(w, http.StatusOK, firecrawlResponse{Success: false, Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, firecrawlResponse{
		Success: true,
		Data:    &firecrawlData{Markdown: page.Markdown, Metadata: page.Metadata},
	})
}

// ── shared JSON writers ─────────────────────────────────────────────────────

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeRaw(w, status, fmt.Sprintf(`{"status":%d,"error":%q}`, status, msg))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "encode error")
		return
	}
	writeRaw(w, status, string(b))
}

func writeRaw(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
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
	native := http.HandlerFunc(searchNative)
	searchDirect := zip.AdaptNetHTTP(native)
	searchKeyed := zip.AdaptNetHTTP(searchGuard(native))
	g := app.Group("/v1/websearch")
	g.All("/search", func(c *zip.Ctx) error {
		if principal.Validated(c) {
			return searchDirect(c)
		}
		return searchKeyed(c)
	})

	scrape := zip.AdaptNetHTTP(http.HandlerFunc(scrapeHandler))
	// Firecrawl builds {apiUrl}/{version}/scrape; pin firecrawlVersion:v1 so the
	// client POSTs /v1/websearch/v1/scrape. Also accept the bare /scrape.
	g.Post("/v1/scrape", scrape)
	g.Post("/scrape", scrape)

	logger.Info("web search surface mounted (native searxng-compat meta-search + firecrawl-compat scrape, both in-process)")
	return nil
}
