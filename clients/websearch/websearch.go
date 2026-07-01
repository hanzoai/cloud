// Package websearch exposes Hanzo-native Web Search + Scrape on the unified
// cloud-api /v1 plane, so hanzo.chat's web_search agent tool runs entirely on
// Hanzo infrastructure with NO external SaaS provider, per HIP-0106.
//
// hanzo.chat (LibreChat fork) implements web_search as a fixed 3-stage pipeline
// whose provider contracts are frozen by the upstream client
// (@librechat/agents tools/search). The only self-hostable, key-less-to-a-SaaS
// providers it accepts are:
//   - search provider  "searxng"   → GET  {searxngInstanceUrl}/search?q=&format=json
//                                     ← {results:[{url,title,content,img_src?}]}
//   - scraper provider "firecrawl" → POST {firecrawlApiUrl}/{version}/scrape
//                                     body {url,formats} ← {success,data:{markdown,metadata}}
//     (reranker is optional; we omit it — provider+scraper is sufficient.)
//
// This subsystem serves BOTH contracts under /v1/websearch, backed by Hanzo's
// own services — never a third-party search API:
//   - GET  /v1/websearch/search        SearXNG-shaped. Proxied to a Hanzo-operated
//                                       metasearch instance (WEBSEARCH_UPSTREAM).
//   - POST /v1/websearch/v1/scrape      Firecrawl-shaped. Backed by Hanzo Crawl
//     (also /v1/websearch/scrape)       (crawl.hanzo.svc, Crawl4AI): fetch the URL,
//                                       return {success,data:{markdown,metadata}}.
//
// The chat server calls these SERVER-SIDE in-cluster, so point
// searxngInstanceUrl / firecrawlApiUrl at this surface (public api.hanzo.ai/v1
// or the internal cloud-api svc DNS — same binary either way).
//
// AUTH: firecrawl requires a Bearer key; searxng accepts an optional X-API-Key.
// Both are the same shared service key WEBSEARCH_API_KEY (KMS-sourced, synced
// into the pod env). An unset key fails closed on the authed scrape path.
package websearch

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/zip"
)

// defaults are in-cluster service DNS; overridable via env.
const (
	// A Hanzo-operated SearXNG metasearch instance. Self-hosted: Hanzo runs it,
	// no third-party search-API key. Serves the SearXNG JSON contract directly,
	// so search is a transparent proxy (no reshape).
	defaultSearchUpstream = "http://searxng.hanzo.svc.cluster.local:8080"
	// Hanzo Crawl (Crawl4AI) — the scrape backend.
	defaultCrawlEndpoint = "http://crawl.hanzo.svc.cluster.local:11235"
)

func searchUpstream() string {
	if v := strings.TrimSpace(os.Getenv("WEBSEARCH_UPSTREAM")); v != "" {
		return v
	}
	return defaultSearchUpstream
}

func crawlEndpoint() string {
	if v := strings.TrimSpace(os.Getenv("WEBSEARCH_CRAWL_ENDPOINT")); v != "" {
		return v
	}
	return defaultCrawlEndpoint
}

// crawlToken is the optional bearer the Hanzo Crawl service expects
// (CRAWL4AI_API_TOKEN on the crawl deployment). Empty ⇒ no header.
func crawlToken() string { return strings.TrimSpace(os.Getenv("WEBSEARCH_CRAWL_TOKEN")) }

// apiKey is the shared service key the chat server presents (firecrawl Bearer /
// searxng X-API-Key). KMS-sourced, synced as WEBSEARCH_API_KEY.
func apiKey() string { return strings.TrimSpace(os.Getenv("WEBSEARCH_API_KEY")) }

var httpClient = &http.Client{Timeout: 45 * time.Second}

// ── SearXNG search: transparent proxy to the Hanzo metasearch instance ──────
// The upstream already speaks the SearXNG JSON contract, so we forward verbatim
// (append nothing, reshape nothing). Only scheme/host/path-prefix change.

func newSearchProxy(rawURL string) (http.Handler, error) {
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if target.Scheme == "" || target.Host == "" {
		return nil, fmt.Errorf("websearch: WEBSEARCH_UPSTREAM must be an absolute URL, got %q", rawURL)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	base := proxy.Director
	proxy.Director = func(r *http.Request) {
		base(r)
		// /v1/websearch/search → {upstream}/search (SearXNG's own path).
		r.URL.Path = "/search" + strings.TrimPrefix(r.URL.Path, "/v1/websearch/search")
		r.URL.RawPath = ""
		r.Host = target.Host
	}
	proxy.Transport = &http.Transport{ResponseHeaderTimeout: 30 * time.Second}
	return proxy, nil
}

// searchGuard applies the OPTIONAL searxng key check: if WEBSEARCH_API_KEY is
// set and the caller sent an X-API-Key, it must match; a missing X-API-Key is
// allowed (searxng treats the key as optional), matching the client which only
// sends it when configured.
func searchGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if want := apiKey(); want != "" {
			if got := strings.TrimSpace(r.Header.Get("X-API-Key")); got != "" &&
				subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				writeErr(w, http.StatusUnauthorized, "invalid api key")
				return
			}
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

// crawlRequest / crawlResult mirror Hanzo Crawl's /crawl contract
// (ai/object/crawl4ai.go).
type crawlRequest struct {
	Urls []string `json:"urls"`
}

type crawlResult struct {
	URL      string                 `json:"url"`
	Markdown string                 `json:"markdown"`
	Success  bool                   `json:"success"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

type crawlResponse struct {
	Status  string        `json:"status"`
	Results []crawlResult `json:"results"`
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

	res, err := crawl(req.URL)
	if err != nil || res == nil || !res.Success {
		msg := "crawl failed"
		if err != nil {
			msg = err.Error()
		}
		writeJSON(w, http.StatusOK, firecrawlResponse{Success: false, Error: msg})
		return
	}
	writeJSON(w, http.StatusOK, firecrawlResponse{
		Success: true,
		Data:    &firecrawlData{Markdown: res.Markdown, Metadata: res.Metadata},
	})
}

// crawl fetches one URL via Hanzo Crawl and returns its markdown result.
func crawl(target string) (*crawlResult, error) {
	body, _ := json.Marshal(crawlRequest{Urls: []string{target}})
	req, err := http.NewRequest(http.MethodPost, crawlEndpoint()+"/crawl", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if tok := crawlToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("hanzo crawl returned %d: %s", resp.StatusCode, string(b))
	}
	var cr crawlResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, err
	}
	if len(cr.Results) == 0 {
		return nil, fmt.Errorf("hanzo crawl returned no results for %s", target)
	}
	return &cr.Results[0], nil
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
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("websearch.Mount: nil zip.App")
	}
	logger := deps.Logger
	if logger == nil {
		return fmt.Errorf("websearch.Mount: nil deps.Logger")
	}
	logger = logger.New("subsystem", "websearch")

	searchProxy, err := newSearchProxy(searchUpstream())
	if err != nil {
		return err
	}
	app.All("/v1/websearch/search", zip.AdaptNetHTTP(searchGuard(searchProxy)))

	scrape := zip.AdaptNetHTTPFunc(scrapeHandler)
	// Firecrawl builds {apiUrl}/{version}/scrape; pin firecrawlVersion:v1 so the
	// client POSTs /v1/websearch/v1/scrape. Also accept the bare /scrape.
	app.Post("/v1/websearch/v1/scrape", scrape)
	app.Post("/v1/websearch/scrape", scrape)

	logger.Info("web search surface mounted (searxng-compat proxy + firecrawl-compat over Hanzo Crawl)",
		"searchUpstream", searchUpstream(), "crawl", crawlEndpoint())
	return nil
}

func init() {
	// Order 141: before hanzoai/ai (150) so /v1/websearch/* wins over ai's
	// /v1/* catch-all; sits next to exec (140).
	cloud.Register("websearch", 141, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("websearch.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	})
}
