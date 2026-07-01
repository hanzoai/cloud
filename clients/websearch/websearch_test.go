package websearch

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fiber "github.com/gofiber/fiber/v3"
	"github.com/hanzoai/cloud"
	"github.com/hanzoai/zip"
	luxlog "github.com/luxfi/log"
)

// Mount() must register /v1/websearch/search + the two scrape POST paths on a
// real Fiber router without panicking, and requests routed through the whole
// app must reach the handlers (search proxy + firecrawl-shaped scrape).
func TestMountRoutesThroughRouter(t *testing.T) {
	searchUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	defer searchUp.Close()
	crawlUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"completed","results":[{"url":"https://ex","markdown":"# M","success":true}]}`)
	}))
	defer crawlUp.Close()
	t.Setenv("WEBSEARCH_UPSTREAM", searchUp.URL)
	t.Setenv("WEBSEARCH_CRAWL_ENDPOINT", crawlUp.URL)
	t.Setenv("WEBSEARCH_API_KEY", "k")

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	fa := app.Fiber()

	// Search routes through to the searxng proxy.
	req := httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai/v1/websearch/search?q=x&format=json", nil)
	resp, err := fa.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("search route: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("search route status %d, want 200", resp.StatusCode)
	}
	_ = resp.Body.Close()

	// Firecrawl scrape (the /v1/scrape path the client builds) routes to the
	// crawl-backed handler and returns the firecrawl shape.
	sreq := httptest.NewRequest(http.MethodPost, "http://api.hanzo.ai/v1/websearch/v1/scrape",
		strings.NewReader(`{"url":"https://ex"}`))
	sreq.Header.Set("Authorization", "Bearer k")
	sreq.Header.Set("Content-Type", "application/json")
	sresp, err := fa.Test(sreq, fiber.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("scrape route: %v", err)
	}
	b, _ := io.ReadAll(sresp.Body)
	if sresp.StatusCode != http.StatusOK || !strings.Contains(string(b), `"success":true`) {
		t.Fatalf("scrape route status %d body %s", sresp.StatusCode, string(b))
	}
	_ = sresp.Body.Close()
}

func TestMountRejectsBadInputs(t *testing.T) {
	if err := Mount(nil, cloud.Deps{Logger: luxlog.New("test")}); err == nil {
		t.Fatal("Mount(nil app) should error")
	}
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{}); err == nil {
		t.Fatal("Mount(nil logger) should error")
	}
}

// Search must proxy /v1/websearch/search → {upstream}/search verbatim (query
// preserved), so the LibreChat searxng client gets a real SearXNG response.
func TestSearchProxyRewritesToSearchPath(t *testing.T) {
	var gotPath, gotQuery string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"results":[{"url":"https://x","title":"T","content":"C"}]}`)
	}))
	defer up.Close()

	proxy, err := newSearchProxy(up.URL)
	if err != nil {
		t.Fatalf("newSearchProxy: %v", err)
	}
	t.Setenv("WEBSEARCH_API_KEY", "") // key optional for search
	h := searchGuard(proxy)

	req := httptest.NewRequest(http.MethodGet,
		"http://api.hanzo.ai/v1/websearch/search?q=hanzo+ai&format=json&engines=google,bing", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if gotPath != "/search" {
		t.Fatalf("upstream path = %q, want /search", gotPath)
	}
	if !strings.Contains(gotQuery, "q=hanzo+ai") || !strings.Contains(gotQuery, "format=json") {
		t.Fatalf("upstream query = %q, want the searxng params preserved", gotQuery)
	}
	if !strings.Contains(rec.Body.String(), `"results"`) {
		t.Fatalf("searxng body not passed through: %s", rec.Body.String())
	}
}

// When a key IS configured and the caller sends a WRONG X-API-Key, reject.
func TestSearchWrongKeyRejected(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("upstream must not be reached with a wrong key")
	}))
	defer up.Close()
	proxy, _ := newSearchProxy(up.URL)
	t.Setenv("WEBSEARCH_API_KEY", "right")
	h := searchGuard(proxy)

	req := httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai/v1/websearch/search?q=x", nil)
	req.Header.Set("X-API-Key", "wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// A missing X-API-Key is allowed for search (searxng key is optional).
func TestSearchMissingKeyAllowed(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"results":[]}`)
	}))
	defer up.Close()
	proxy, _ := newSearchProxy(up.URL)
	t.Setenv("WEBSEARCH_API_KEY", "configured")
	h := searchGuard(proxy)

	req := httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai/v1/websearch/search?q=x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (missing key allowed for searxng)", rec.Code)
	}
}

// Scrape must Bearer-auth, call Hanzo Crawl, and adapt its {url,markdown,...}
// result into the firecrawl {success,data:{markdown,metadata}} shape.
func TestScrapeAdaptsCrawlToFirecrawlShape(t *testing.T) {
	var gotCrawlBody string
	crawlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/crawl" {
			t.Fatalf("crawl path = %q, want /crawl", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		gotCrawlBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"completed","results":[{"url":"https://ex.com","markdown":"# Hello","success":true,"metadata":{"title":"Ex"}}]}`)
	}))
	defer crawlSrv.Close()

	t.Setenv("WEBSEARCH_API_KEY", "svc-key")
	t.Setenv("WEBSEARCH_CRAWL_ENDPOINT", crawlSrv.URL)

	req := httptest.NewRequest(http.MethodPost, "http://api.hanzo.ai/v1/websearch/v1/scrape",
		strings.NewReader(`{"url":"https://ex.com","formats":["markdown"]}`))
	req.Header.Set("Authorization", "Bearer svc-key")
	rec := httptest.NewRecorder()
	scrapeHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if !strings.Contains(gotCrawlBody, `"urls":["https://ex.com"]`) {
		t.Fatalf("crawl body = %q, want urls array with the target", gotCrawlBody)
	}
	var out firecrawlResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v (%s)", err, rec.Body.String())
	}
	if !out.Success || out.Data == nil || out.Data.Markdown != "# Hello" {
		t.Fatalf("response = %+v, want success with markdown '# Hello'", out)
	}
	if out.Data.Metadata["title"] != "Ex" {
		t.Fatalf("metadata not passed through: %+v", out.Data.Metadata)
	}
}

// Scrape fails closed with no configured key.
func TestScrapeUnsetKeyFailsClosed(t *testing.T) {
	t.Setenv("WEBSEARCH_API_KEY", "")
	req := httptest.NewRequest(http.MethodPost, "http://api.hanzo.ai/v1/websearch/v1/scrape",
		strings.NewReader(`{"url":"https://ex.com"}`))
	req.Header.Set("Authorization", "Bearer anything")
	rec := httptest.NewRecorder()
	scrapeHandler(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (fail closed)", rec.Code)
	}
}

// Scrape rejects a wrong Bearer key.
func TestScrapeWrongKeyRejected(t *testing.T) {
	t.Setenv("WEBSEARCH_API_KEY", "right")
	req := httptest.NewRequest(http.MethodPost, "http://api.hanzo.ai/v1/websearch/v1/scrape",
		strings.NewReader(`{"url":"https://ex.com"}`))
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	scrapeHandler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestNewSearchProxyRejectsBadURL(t *testing.T) {
	if _, err := newSearchProxy("://nope"); err == nil {
		t.Fatal("expected error for malformed upstream URL")
	}
	if _, err := newSearchProxy("relative"); err == nil {
		t.Fatal("expected error for non-absolute upstream URL")
	}
}

func TestUpstreamDefaultsAndOverrides(t *testing.T) {
	t.Setenv("WEBSEARCH_UPSTREAM", "")
	if got := searchUpstream(); got != defaultSearchUpstream {
		t.Fatalf("searchUpstream() = %q, want default", got)
	}
	t.Setenv("WEBSEARCH_UPSTREAM", "http://searxng:8080")
	if got := searchUpstream(); got != "http://searxng:8080" {
		t.Fatalf("searchUpstream() override = %q", got)
	}
	t.Setenv("WEBSEARCH_CRAWL_ENDPOINT", "")
	if got := crawlEndpoint(); got != defaultCrawlEndpoint {
		t.Fatalf("crawlEndpoint() = %q, want default", got)
	}
}
