package websearch

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
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

// served drives one request through the LIVE router — the registration Mount
// makes in a plugin binary, never a handler reconstructed beside it.
//
// Both compat endpoints answer off the zip Ctx, so there is no http.Handler to
// call directly any more and no reason to want one: a test that built its own
// ResponseRecorder measured a function, and what these two endpoints owe is a
// wire somebody else's client reads.
func served(t *testing.T, app *zip.App, method, target, body string, hdr map[string]string) (int, string) {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second})
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(raw))
}

// Mount() must register /v1/websearch/search + the two scrape POST paths on a real
// Fiber router without panicking, and requests routed through the whole app must
// reach the native search handler + firecrawl-shaped scrape.
func TestMountRoutesThroughRouter(t *testing.T) {
	mockBing(t, bingFixture)
	t.Setenv("WEBSEARCH_API_KEY", "k")
	app := mounted(t)

	// Search routes through to native meta-search. The client presents the shared
	// key as X-API-Key (searchGuard requires it, like the scrape sibling). The
	// response is the SearXNG envelope built in-process from the mocked engine.
	code, body := served(t, app, http.MethodGet, "/v1/websearch/search?q=x&format=json", "",
		map[string]string{"X-API-Key": "k"})
	if code != http.StatusOK {
		t.Fatalf("search route status %d, want 200", code)
	}
	if !strings.Contains(body, "https://example.com/page") {
		t.Fatalf("native search did not return the mocked result: %s", body)
	}

	// The scrape endpoint routes to the in-process crawl handler and answers in the
	// firecrawl shape — the envelope is firecrawl's, the address is ours.
	//
	// The URL is deliberately one that cannot be fetched, and the assertion is on
	// the ENVELOPE, not on success. What this test owns is that the route exists,
	// the key is accepted, and the reply decodes as firecrawl — asserting a live
	// fetch here would make a router test depend on the network and on some third
	// party's uptime. That scrape maps a failed fetch to success:false is asserted
	// in TestScrapeReportsFetchFailure, and the fetch itself is covered in
	// clients/crawl.
	scode, sbody := served(t, app, http.MethodPost, "/v1/websearch/scrape", `{"url":"https://ex"}`,
		map[string]string{"Authorization": "Bearer k", "Content-Type": "application/json"})
	if scode != http.StatusOK {
		t.Fatalf("scrape route status %d body %s — the route must be reachable with a valid key", scode, sbody)
	}
	var env firecrawlResponse
	if err := json.Unmarshal([]byte(sbody), &env); err != nil {
		t.Fatalf("scrape reply is not the firecrawl envelope: %v (%s)", err, sbody)
	}
}

// THE CONTENT TYPE IS THE BARE `application/json` and the answer carries its own
// LENGTH, which is the one measurable thing the adaptor's removal moved.
//
// Every reply used to leave through a pipe the net/http adaptor set as a body
// STREAM of unknown size, so fasthttp framed it chunked. Native, fasthttp writes
// Content-Length. Status, bytes and Content-Type are unchanged — and the type is
// asserted because a reach for c.JSON would silently make it
// `application/json; charset=utf-8`, a header two clients we do not own read.
func TestCompatRepliesKeepTheirFramingAndType(t *testing.T) {
	t.Setenv("WEBSEARCH_API_KEY", "k")
	app := mounted(t)

	for _, tc := range []struct{ name, method, target, body string }{
		{"search refusal", http.MethodGet, "/v1/websearch/search?q=x", ""},
		{"scrape refusal", http.MethodPost, "/v1/websearch/scrape", `{"url":"https://ex"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rd io.Reader
			if tc.body != "" {
				rd = strings.NewReader(tc.body)
			}
			req := httptest.NewRequest(tc.method, tc.target, rd)
			resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second})
			if err != nil {
				t.Fatalf("%s %s: %v", tc.method, tc.target, err)
			}
			defer func() { _ = resp.Body.Close() }()
			raw, _ := io.ReadAll(resp.Body)

			if got := resp.Header.Get("Content-Type"); got != "application/json" {
				t.Errorf("Content-Type = %q, want the bare %q these compat clients have always been sent",
					got, "application/json")
			}
			if resp.ContentLength != int64(len(raw)) {
				t.Errorf("Content-Length = %d over a %d-byte body — a native reply states its length",
					resp.ContentLength, len(raw))
			}
		})
	}
}

// Scrape reports an unfetchable URL as a 200 carrying success:false, not as an
// HTTP error. The firecrawl client treats a non-2xx as a broken provider and can
// disable the tool; "that page could not be read" is an answer, not a fault of the
// request. A URL the address guard refuses is used because it fails identically on
// every machine and needs no network.
func TestScrapeReportsFetchFailure(t *testing.T) {
	t.Setenv("WEBSEARCH_API_KEY", "svc-key")
	app := mounted(t)

	code, body := served(t, app, http.MethodPost, "/v1/websearch/scrape", `{"url":"http://127.0.0.1:1/"}`,
		map[string]string{"Authorization": "Bearer svc-key"})
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 even when the fetch fails", code)
	}
	var out firecrawlResponse
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if out.Success {
		t.Fatal("success = true for a URL that cannot be fetched")
	}
	if out.Error == "" {
		t.Fatal("no error message — a caller debugging a failed scrape has nothing to go on")
	}
}

// THE BODY IS TOLERATED, AND THAT IS THE WIRE. A body that is not JSON, one that
// is empty, and one past the 1 MiB cap all answer 200 success:false — a firecrawl
// client reads data.success, not the status line. The cap moved from an
// io.LimitReader over the request stream to a bound on c.Body(); this is what
// says the move did not change which bodies are accepted.
func TestScrapeToleratesTheBodyItCannotRead(t *testing.T) {
	t.Setenv("WEBSEARCH_API_KEY", "svc-key")
	app := mounted(t)
	auth := map[string]string{"Authorization": "Bearer svc-key"}

	// A url past the cap: the truncated slice cannot close its JSON value.
	oversized := `{"url":"https://example.com/` + strings.Repeat("a", maxScrapeBody) + `"}`

	for _, tc := range []struct{ name, body string }{
		{"not json", "<html>not json at all</html>"},
		{"empty", ""},
		{"no url", `{"formats":["markdown"]}`},
		{"past the cap", oversized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := served(t, app, http.MethodPost, "/v1/websearch/scrape", tc.body, auth)
			if code != http.StatusOK {
				t.Fatalf("status = %d %s, want 200 — a refusal here is a DOMAIN answer", code, body)
			}
			if body != `{"success":false,"error":"missing url"}` {
				t.Fatalf("body = %s, want the firecrawl refusal verbatim", body)
			}
		})
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
	app := mounted(t)

	// X-User-Id is set only by the identity middleware from a verified JWT.
	code, body := served(t, app, http.MethodGet, "/v1/websearch/search?q=x&format=json", "",
		map[string]string{"X-User-Id": "user-123"})
	if code != http.StatusOK {
		t.Fatalf("validated-principal search status %d %s, want 200 (must bypass the shared key)", code, body)
	}
	if !reached {
		t.Fatal("native search engine was not reached for a validated principal")
	}
}

// F2 STILL HOLDS at the router: a caller with NO validated principal AND no key is
// refused — the principal path did not reopen the open-surface hole. With the key
// unset the key path fails closed (503); the anonymous caller never reaches search.
func TestSearchNoPrincipalNoKeyRefused(t *testing.T) {
	t.Setenv("WEBSEARCH_API_KEY", "")
	app := mounted(t)

	if code, body := served(t, app, http.MethodGet, "/v1/websearch/search?q=x", "", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("anonymous no-key search status %d %s, want 503 (fail closed, no open surface)", code, body)
	}
}

func TestMountRejectsBadInputs(t *testing.T) {
	if err := Mount(nil, cloud.Deps{}); err == nil {
		t.Fatal("Mount(nil app) should error")
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

// The key gate on BOTH compat endpoints, driven through the live router: unset is
// 503 whatever the caller presents, and a missing or wrong credential is 401.
//
// It is ONE table because the two endpoints answer one rule in two headers — search
// reads X-API-Key, scrape a Bearer — and the ORDER is the part worth pinning:
// 503-before-401, and both decided before any body is read, which is what a
// typed op could not express (the decode runs before the handler is entered).
func TestTheKeyGateIsTheWire(t *testing.T) {
	const url = `{"url":"https://ex.com"}`
	for _, tc := range []struct {
		name, key, method, target, body string
		hdr                             map[string]string
		want                            int
	}{
		{"search wrong key", "right", http.MethodGet, "/v1/websearch/search?q=x", "",
			map[string]string{"X-API-Key": "wrong"}, http.StatusUnauthorized},
		// SECURITY (F2): a MISSING X-API-Key must be REJECTED — /v1/websearch/search
		// is not an open surface. It fails closed exactly like the scrape sibling.
		{"search missing key", "configured", http.MethodGet, "/v1/websearch/search?q=x", "",
			nil, http.StatusUnauthorized},
		{"search unset key", "", http.MethodGet, "/v1/websearch/search?q=x", "",
			map[string]string{"X-API-Key": "anything"}, http.StatusServiceUnavailable},
		{"scrape wrong key", "right", http.MethodPost, "/v1/websearch/scrape", url,
			map[string]string{"Authorization": "Bearer wrong"}, http.StatusUnauthorized},
		{"scrape unset key", "", http.MethodPost, "/v1/websearch/scrape", url,
			map[string]string{"Authorization": "Bearer anything"}, http.StatusServiceUnavailable},
		// The credential is asked BEFORE the body, so a caller with no key never
		// buys a parse — and a garbage body is still 401 rather than the domain
		// refusal the same body earns from an authorized caller.
		{"scrape unauthorized garbage body", "right", http.MethodPost, "/v1/websearch/scrape",
			"<not json>", map[string]string{"Authorization": "Bearer wrong"}, http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("WEBSEARCH_API_KEY", tc.key)
			app := mounted(t)
			code, body := served(t, app, tc.method, tc.target, tc.body, tc.hdr)
			if code != tc.want {
				t.Fatalf("status = %d %s, want %d", code, body, tc.want)
			}
			// The refusal keeps the compat vocabulary. A returned zip error would
			// render RFC 9457 problem-details and move the sentence to `detail`.
			if !strings.Contains(body, `"error"`) || !strings.Contains(body, `"status"`) {
				t.Fatalf("refusal body = %s, want the compat {\"status\":…,\"error\":…}", body)
			}
		})
	}
}

// mojeekFixture is a Mojeek result page as the CLUSTER receives it: an <li> per
// hit carrying <a class="title"> (the destination, verbatim — Mojeek uses no
// click-tracking redirect) and <p class="s"> (the snippet).
const mojeekFixture = `<html><body><ul class="results-standard">
<li class="r1"><a title="https://example.com/one" href="https://example.com/one" class="ob"><p class="i"><span class="url">https://example.com</span></p></a><h2><a class="title" href="https://example.com/one">First Title</a></h2><p class="s">The <strong>first</strong> snippet.</p></li>
<li class="r2 clu-result"><a title="https://example.com/two" href="https://example.com/two" class="ob"></a><h2><a class="title" href="https://example.com/two">Second Title</a></h2><p class="s">The second snippet.</p></li>
</ul></body></html>`

// Mojeek is the engine that carries the `site:` operator, which is how an X /
// GitHub / Reddit scoped search reaches results at all — Bing answers a
// site:x.com query with nothing. Measured from cluster egress 2026-08-07:
// bing site:x.com → 0 results, mojeek site:x.com → 10.
func TestParseMojeekReadsTitleURLAndSnippet(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, mojeekFixture)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("WEBSEARCH_ENGINES", "mojeek")
	t.Setenv("WEBSEARCH_MOJEEK_URL", srv.URL)

	got := metaSearch(context.Background(), "anything", "")
	if len(got.Results) != 2 {
		t.Fatalf("results = %d, want 2 — parseMojeek must read every <li> hit: %+v", len(got.Results), got.Results)
	}
	r := got.Results[0]
	if r.URL != "https://example.com/one" || r.Title != "First Title" || r.Engine != "mojeek" {
		t.Fatalf("parsed = %+v, want the fixture's url/title stamped engine=mojeek", r)
	}
	if !strings.Contains(r.Content, "first") {
		t.Fatalf("content = %q, want the <p class=\"s\"> snippet", r.Content)
	}
}

// A deployment that configures NOTHING must still search more than one engine.
// WEBSEARCH_ENGINES was unset in production, so the default WAS the whole engine
// set, and it was bing alone — one engine, 10 results, and no `site:` support.
func TestDefaultEnginesAreEveryEngineThatSurvivesDatacenterEgress(t *testing.T) {
	t.Setenv("WEBSEARCH_ENGINES", "")
	var names []string
	for _, e := range enabledEngines() {
		names = append(names, e.name)
	}
	if len(names) < 2 {
		t.Fatalf("default engines = %v, want more than one — a single engine is a single point of failure and a single index", names)
	}
	// Every engine measured to ANSWER from cluster egress, by whichever fetch it
	// takes. DDG belongs here on the rendered number, not the static one: served
	// a captcha over plain HTTP ("Select all squares containing a duck", 0
	// results) and 10 real results through the browser at the same URL, the same
	// second. render.go escalates, so the engine answers.
	//
	// It is safe to default an engine that DEPENDS on the browser only because a
	// browser-less deployment now reports it BLIND rather than dropping it
	// quietly — see outcome.go. Without that this line would be optimism.
	for _, want := range []string{bingName, ddgName, mojeekName} {
		if !slices.Contains(names, want) {
			t.Fatalf("default engines = %v, want %q among them", names, want)
		}
	}
}
