package websearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	fiber "github.com/gofiber/fiber/v3"
	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// bingFixture is a minimal Bing result page parseBing understands: a b_algo block
// with an <h2><a> (absolute non-ck/a href passes through verbatim) and a snippet.
const bingFixture = `<html><body>
<li class="b_algo">
  <h2><a href="https://example.com/page">Example Title</a></h2>
  <p class="b_lineclamp2">A snippet of the result.</p>
</li>
</body></html>`

// mockBing points the bing engine at a local server serving fixture HTML and pins
// the engine set to bing only, so metaSearch is fully offline + deterministic.
func mockBing(t *testing.T, html string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, html)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("WEBSEARCH_ENGINES", "bing")
	t.Setenv("WEBSEARCH_BING_URL", srv.URL)
	return srv
}

// okHandler is a trivial next-handler for exercising searchGuard in isolation
// (the guard rejects before next runs, so it never actually fires on reject paths).
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

// Mount() must register /v1/websearch/search + the two scrape POST paths on a real
// Fiber router without panicking, and requests routed through the whole app must
// reach the native search handler + firecrawl-shaped scrape.
func TestMountRoutesThroughRouter(t *testing.T) {
	mockBing(t, bingFixture)
	crawlUp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"completed","results":[{"url":"https://ex","markdown":"# M","success":true}]}`)
	}))
	defer crawlUp.Close()
	t.Setenv("WEBSEARCH_CRAWL_ENDPOINT", crawlUp.URL)
	t.Setenv("WEBSEARCH_API_KEY", "k")

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	fa := app.Fiber()

	// Search routes through to native meta-search. The client presents the shared
	// key as X-API-Key (searchGuard requires it, like the scrape sibling). The
	// response is the SearXNG envelope built in-process from the mocked engine.
	req := httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai/v1/websearch/search?q=x&format=json", nil)
	req.Header.Set("X-API-Key", "k")
	resp, err := fa.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("search route: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("search route status %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), "https://example.com/page") {
		t.Fatalf("native search did not return the mocked result: %s", string(body))
	}

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

// A signed-in console user reaches search WITHOUT the shared key: the identity
// middleware set X-User-Id (principal.Validated), so the zip-layer gate runs
// native search even with WEBSEARCH_API_KEY unset. This is the console
// user-bearer path the /cloud proxy drives.
func TestSearchValidatedPrincipalBypassesKey(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, bingFixture)
	}))
	defer srv.Close()
	t.Setenv("WEBSEARCH_ENGINES", "bing")
	t.Setenv("WEBSEARCH_BING_URL", srv.URL)
	t.Setenv("WEBSEARCH_API_KEY", "") // unset: the key path would 503 — the principal must pass regardless

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	fa := app.Fiber()

	req := httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai/v1/websearch/search?q=x&format=json", nil)
	req.Header.Set("X-User-Id", "user-123") // set only by the identity middleware from a verified JWT
	resp, err := fa.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("search route: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("validated-principal search status %d, want 200 (must bypass the shared key)", resp.StatusCode)
	}
	_ = resp.Body.Close()
	if !reached {
		t.Fatal("native search engine was not reached for a validated principal")
	}
}

// F2 STILL HOLDS at the router: a caller with NO validated principal AND no key is
// refused — the principal path did not reopen the open-surface hole. With the key
// unset the key path fails closed (503); the anonymous caller never reaches search.
func TestSearchNoPrincipalNoKeyRefused(t *testing.T) {
	t.Setenv("WEBSEARCH_API_KEY", "")

	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	if err := Mount(app, cloud.Deps{Logger: luxlog.New("test")}); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	fa := app.Fiber()

	req := httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai/v1/websearch/search?q=x", nil)
	resp, err := fa.Test(req, fiber.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("search route: %v", err)
	}
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("anonymous no-key search status %d, want 503 (fail closed, no open surface)", resp.StatusCode)
	}
	_ = resp.Body.Close()
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

// metaSearch runs the enabled keyless engines in-process and returns the SearXNG
// envelope. With bing mocked to fixture HTML it parses exactly one result — no
// network, no SearXNG pod.
func TestMetaSearchParsesEngineResult(t *testing.T) {
	mockBing(t, bingFixture)
	got := metaSearch(context.Background(), "hanzo ai", "")
	if got.Query != "hanzo ai" {
		t.Fatalf("query = %q, want echoed", got.Query)
	}
	if len(got.Results) != 1 || got.NumberOfResults != 1 {
		t.Fatalf("results = %+v, want exactly 1", got.Results)
	}
	r := got.Results[0]
	if r.URL != "https://example.com/page" || r.Title != "Example Title" || r.Engine != "bing" {
		t.Fatalf("parsed result = %+v, want the fixture's url/title/engine", r)
	}
	if !strings.Contains(r.Content, "snippet") {
		t.Fatalf("content = %q, want the snippet", r.Content)
	}
}

// A challenged/failing engine (non-200) contributes zero and never fails the
// request — search degrades to an empty-but-valid envelope, never a 5xx.
func TestMetaSearchDegradesOnEngineFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden) // bot-challenge / rate-limit
	}))
	defer srv.Close()
	t.Setenv("WEBSEARCH_ENGINES", "bing")
	t.Setenv("WEBSEARCH_BING_URL", srv.URL)

	got := metaSearch(context.Background(), "x", "")
	if got.Results == nil {
		t.Fatal("results must be a non-nil array, never null")
	}
	if len(got.Results) != 0 {
		t.Fatalf("results = %+v, want empty on engine failure", got.Results)
	}
}

// When a key IS configured and the caller sends a WRONG X-API-Key, reject.
func TestSearchWrongKeyRejected(t *testing.T) {
	t.Setenv("WEBSEARCH_API_KEY", "right")
	h := searchGuard(okHandler)

	req := httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai/v1/websearch/search?q=x", nil)
	req.Header.Set("X-API-Key", "wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

// SECURITY (F2): a MISSING X-API-Key must be REJECTED — /v1/websearch/search is
// not an open surface. It fails closed exactly like the scrape sibling.
func TestSearchMissingKeyRejected(t *testing.T) {
	t.Setenv("WEBSEARCH_API_KEY", "configured")
	h := searchGuard(okHandler)

	req := httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai/v1/websearch/search?q=x", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (missing key must be rejected — no open surface)", rec.Code)
	}
}

// Search fails closed with no configured key (503), mirroring scrape.
func TestSearchUnsetKeyFailsClosed(t *testing.T) {
	t.Setenv("WEBSEARCH_API_KEY", "")
	h := searchGuard(okHandler)

	req := httptest.NewRequest(http.MethodGet, "http://api.hanzo.ai/v1/websearch/search?q=x", nil)
	req.Header.Set("X-API-Key", "anything")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (fail closed when unconfigured)", rec.Code)
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

// REGRESSION: the deployed hanzoai/crawl:0.0.1 (Crawl4AI 0.8.6) returns `markdown`
// as an OBJECT {raw_markdown, fit_markdown, ...}, not a bare string, AND signals
// the batch with a boolean "success" (no "status"). A `string` Markdown field
// errored the whole decode → crawl() failed → every scrape returned {success:false}.
// This asserts the real 0.8.6 envelope + object markdown adapt correctly.
func TestScrapeHandlesCrawl4AIObjectMarkdown(t *testing.T) {
	crawlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{
			"success": true,
			"server_processing_time_s": 1.2,
			"results": [{
				"url": "https://ex.com",
				"success": true,
				"status_code": 200,
				"markdown": {
					"raw_markdown": "# Raw Hello\n\nlots of nav noise",
					"fit_markdown": "# Hello\n\nclean body",
					"markdown_with_citations": "# Hello [1]",
					"references_markdown": "[1]: https://ex.com"
				},
				"metadata": {"title": "Ex", "description": "d"}
			}]
		}`)
	}))
	defer crawlSrv.Close()

	t.Setenv("WEBSEARCH_API_KEY", "svc-key")
	t.Setenv("WEBSEARCH_CRAWL_ENDPOINT", crawlSrv.URL)

	req := httptest.NewRequest(http.MethodPost, "http://api.hanzo.ai/v1/websearch/v1/scrape",
		strings.NewReader(`{"url":"https://ex.com"}`))
	req.Header.Set("Authorization", "Bearer svc-key")
	rec := httptest.NewRecorder()
	scrapeHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body.String())
	}
	var out firecrawlResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode response: %v (%s)", err, rec.Body.String())
	}
	if !out.Success || out.Data == nil {
		t.Fatalf("response = %+v, want success:true with data (object markdown must decode)", out)
	}
	if out.Data.Markdown != "# Hello\n\nclean body" {
		t.Fatalf("markdown = %q, want the fit_markdown '# Hello\\n\\nclean body'", out.Data.Markdown)
	}
	if out.Data.Metadata["title"] != "Ex" {
		t.Fatalf("metadata not passed through: %+v", out.Data.Metadata)
	}
}

// The bare-string markdown form (older mirror / other crawlers) must still work.
func TestMarkdownFieldAcceptsBareString(t *testing.T) {
	var r crawlResult
	if err := json.Unmarshal([]byte(`{"url":"u","markdown":"# S","success":true}`), &r); err != nil {
		t.Fatalf("decode bare-string markdown: %v", err)
	}
	if string(r.Markdown) != "# S" {
		t.Fatalf("markdown = %q, want '# S'", r.Markdown)
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

// crawlEndpoint honors its default and the env override.
func TestCrawlEndpointDefaultsAndOverrides(t *testing.T) {
	t.Setenv("WEBSEARCH_CRAWL_ENDPOINT", "")
	if got := crawlEndpoint(); got != defaultCrawlEndpoint {
		t.Fatalf("crawlEndpoint() = %q, want default", got)
	}
	t.Setenv("WEBSEARCH_CRAWL_ENDPOINT", "http://crawl:11235")
	if got := crawlEndpoint(); got != "http://crawl:11235" {
		t.Fatalf("crawlEndpoint() override = %q", got)
	}
}
