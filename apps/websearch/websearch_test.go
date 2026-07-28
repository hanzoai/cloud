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

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	fiber "github.com/zap-proto/fiber/v3"
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
	// in-process crawl handler and answers in the firecrawl shape.
	//
	// The URL is deliberately one that cannot be fetched, and the assertion is on
	// the ENVELOPE, not on success. What this test owns is that the route exists,
	// the key is accepted, and the reply decodes as firecrawl — asserting a live
	// fetch here would make a router test depend on the network and on some third
	// party's uptime. That scrape maps a failed fetch to success:false is asserted
	// in TestScrapeReportsFetchFailure, and the fetch itself is covered in
	// clients/crawl.
	sreq := httptest.NewRequest(http.MethodPost, "http://api.hanzo.ai/v1/websearch/v1/scrape",
		strings.NewReader(`{"url":"https://ex"}`))
	sreq.Header.Set("Authorization", "Bearer k")
	sreq.Header.Set("Content-Type", "application/json")
	sresp, err := fa.Test(sreq, fiber.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("scrape route: %v", err)
	}
	b, _ := io.ReadAll(sresp.Body)
	_ = sresp.Body.Close()
	if sresp.StatusCode != http.StatusOK {
		t.Fatalf("scrape route status %d body %s — the route must be reachable with a valid key", sresp.StatusCode, string(b))
	}
	var env firecrawlResponse
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("scrape reply is not the firecrawl envelope: %v (%s)", err, string(b))
	}
}

// Scrape reports an unfetchable URL as a 200 carrying success:false, not as an
// HTTP error. The firecrawl client treats a non-2xx as a broken provider and can
// disable the tool; "that page could not be read" is an answer, not a fault of the
// request. A URL the address guard refuses is used because it fails identically on
// every machine and needs no network.
func TestScrapeReportsFetchFailure(t *testing.T) {
	t.Setenv("WEBSEARCH_API_KEY", "svc-key")

	req := httptest.NewRequest(http.MethodPost, "http://api.hanzo.ai/v1/websearch/v1/scrape",
		strings.NewReader(`{"url":"http://127.0.0.1:1/"}`))
	req.Header.Set("Authorization", "Bearer svc-key")
	rec := httptest.NewRecorder()
	scrapeHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when the fetch fails", rec.Code)
	}
	var out firecrawlResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if out.Success {
		t.Fatal("success = true for a URL that cannot be fetched")
	}
	if out.Error == "" {
		t.Fatal("no error message — a caller debugging a failed scrape has nothing to go on")
	}
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
