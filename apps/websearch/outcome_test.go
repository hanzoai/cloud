package websearch

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
)

// rotted is a search-results page that RENDERED PERFECTLY and that our parser
// cannot read: real markup, real anchors, real snippets, one class name changed.
// This is what selector rot looks like from the inside — it is exactly why Brave
// was excluded rather than parsed, its classes being Svelte build hashes that
// change on every deploy.
//
// The bytes matter. A challenge page and a rotted page are both "a 200 with no
// results we can see", and neither is short: DDG's measured challenge page is
// 25,672 bytes of real HTML that says "Select all squares containing a duck".
const rotted = `<html><head><title>Results</title></head><body>
<div class="results">
  <table>
    <tr><td><a rel="nofollow" href="https://example.com/one" class="result-link-v2">First Real Result</a></td></tr>
    <tr><td class="result-snippet-v2">A snippet that a person reading this page would see.</td></tr>
    <tr><td><a rel="nofollow" href="https://example.com/two" class="result-link-v2">Second Real Result</a></td></tr>
    <tr><td class="result-snippet-v2">Another snippet, also plainly visible to a person.</td></tr>
  </table>
</div></body></html>`

// engineServing replies to every request with body, and points the named engine
// at it.
func engineServing(t *testing.T, env, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	t.Setenv(env, srv.URL)
	return srv
}

// browserServing stands in for Hanzo Crawl, answering /crawl with body as the
// rendered HTML.
func browserServing(t *testing.T, body string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results": []map[string]any{{"html": body, "success": true}},
		})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("CRAWL_URL", srv.URL)
	t.Setenv("WEBSEARCH_RENDER", "on")
}

// capture points the package logger at a buffer and returns it.
func capture(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	setLogger(luxlog.New("test").Output(buf))
	t.Cleanup(func() { setLogger(nil) })
	return buf
}

// THE TEST THIS FILE EXISTS FOR.
//
// The page rendered — in a real browser, at that — and the parser read nothing
// out of it. That is a FAULT, and before the outcome existed it was reported as
// zero results with no error: the same value as a query the web has no answer
// for. An engine could rot away completely and the only symptom was a shorter
// page.
//
// browsed must be true. We did not merely fail to fetch; we drew the page and
// still could not read it, which rules out the network and points at the parser.
func TestRenderedButUnparsedPageIsBlindNotEmpty(t *testing.T) {
	cacheReset()
	engineServing(t, "WEBSEARCH_DDG_URL", rotted)
	browserServing(t, rotted)
	t.Setenv("WEBSEARCH_ENGINES", "ddg")

	got := metaSearch(context.Background(), "anything", "")

	if len(got.Engines) != 1 {
		t.Fatalf("engines = %+v, want one entry for the one engine asked", got.Engines)
	}
	e := got.Engines[0]
	if e.Outcome != string(blind) {
		t.Fatalf("outcome = %q, want %q — a page that rendered and parsed to nothing is a fault, not an empty answer", e.Outcome, blind)
	}
	if e.Name != ddgName {
		t.Fatalf("engine name = %q, want %q", e.Name, ddgName)
	}
	// And the answer still stands: a blind engine must not fail the request.
	if got.Results == nil {
		t.Fatal("results must be a non-nil array even when every engine went blind")
	}
}

// The browser having RUN is part of the report, because it decides who is woken.
// browsed=false is a configuration fault (escalation off, or crawl unreachable);
// browsed=true is a parser fault. Collapsing them buries the loudest signal this
// package has under "not switched on".
func TestBlindSaysWhetherTheBrowserHadAlreadyRun(t *testing.T) {
	cacheReset()
	engineServing(t, "WEBSEARCH_DDG_URL", rotted)
	t.Setenv("WEBSEARCH_ENGINES", "ddg")

	// Escalation off: the browser never ran.
	t.Setenv("WEBSEARCH_RENDER", "")
	off := fetchEngine(context.Background(), ddgEngine, "q", "")
	if off.outcome != blind || off.browsed {
		t.Fatalf("render off: outcome=%q browsed=%v, want blind and browsed=false", off.outcome, off.browsed)
	}

	// Escalation on and the render still unreadable: the browser ran.
	cacheReset()
	browserServing(t, rotted)
	on := fetchEngine(context.Background(), ddgEngine, "q", "")
	if on.outcome != blind || !on.browsed {
		t.Fatalf("render on: outcome=%q browsed=%v, want blind and browsed=true", on.outcome, on.browsed)
	}
}

// A bot challenge is the case the browser exists for, and it is measured, not
// imagined: lite.duckduckgo.com served this cluster 25,672 bytes reading
// "Unfortunately, bots use DuckDuckGo too. Select all squares containing a duck"
// over static HTTP, and 10 real results through the browser at the same second.
func TestChallengedEngineRecoversThroughTheBrowser(t *testing.T) {
	cacheReset()
	const challenge = `<html><body><h1>Unfortunately, bots use DuckDuckGo too.</h1>
	<p>Please complete the following challenge to confirm this search was made by a human.</p>
	<form><input name="duck"><input type="submit"></form></body></html>`
	const real = `<html><body><table>
	<tr><td><a rel="nofollow" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgithub.com%2Ffirecracker-microvm%2Ffirecracker" class="result-link">firecracker-microvm/firecracker</a></td></tr>
	<tr><td class="result-snippet">Secure and fast microVMs.</td></tr></table></body></html>`

	engineServing(t, "WEBSEARCH_DDG_URL", challenge)
	browserServing(t, real)
	t.Setenv("WEBSEARCH_ENGINES", "ddg")

	got := metaSearch(context.Background(), "firecracker microvm", "")
	if len(got.Results) != 1 {
		t.Fatalf("results = %+v, want the browser's one hit to survive the challenge", got.Results)
	}
	if got.Results[0].URL != "https://github.com/firecracker-microvm/firecracker" {
		t.Fatalf("url = %q, want the uddg target unwrapped", got.Results[0].URL)
	}
	if got.Engines[0].Outcome != string(answered) {
		t.Fatalf("outcome = %q, want %q — the browser read it", got.Engines[0].Outcome, answered)
	}
}

// THE 202. DuckDuckGo serves its bot challenge under a 2xx — measured three
// times from cluster egress, HTTP 202 with 14,180 bytes of "Select all squares
// containing a duck" — and the fetch used to accept only 200. So the challenge
// became a transport error, the transport error short-circuited past the
// escalation, and the one engine the browser was deployed to rescue was the one
// engine that could never reach it. DDG read `failed` on every live query.
func TestChallengeUnderA202StillReachesTheBrowser(t *testing.T) {
	cacheReset()
	const challenge = `<html><body><h1>Unfortunately, bots use DuckDuckGo too.</h1></body></html>`
	const real = `<html><body><table>
	<tr><td><a href="https://example.com/rescued" class="result-link">Rescued</a></td></tr>
	<tr><td class="result-snippet">via the browser</td></tr></table></body></html>`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted) // 202, exactly as DDG serves it
		_, _ = w.Write([]byte(challenge))
	}))
	defer srv.Close()
	t.Setenv("WEBSEARCH_DDG_URL", srv.URL)
	browserServing(t, real)
	t.Setenv("WEBSEARCH_ENGINES", "ddg")

	got := metaSearch(context.Background(), "q", "")
	if got.Engines[0].Outcome != string(answered) {
		t.Fatalf("outcome = %q, want %q — a 202 carries a body worth parsing and, when it holds no results, worth rendering", got.Engines[0].Outcome, answered)
	}
	if len(got.Results) != 1 || got.Results[0].URL != "https://example.com/rescued" {
		t.Fatalf("results = %+v, want the browser's hit", got.Results)
	}
}

// A 2xx that ALREADY carries results is parsed and kept, with no render — the
// status widening must not turn a good answer into an escalation.
func TestNon200SuccessIsParsedWithoutRendering(t *testing.T) {
	cacheReset()
	const good = `<html><body><li class="b_algo"><h2><a href="https://example.com/hit">Hit</a></h2><p>s</p></li></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNonAuthoritativeInfo) // 203
		_, _ = w.Write([]byte(good))
	}))
	defer srv.Close()
	t.Setenv("WEBSEARCH_BING_URL", srv.URL)
	t.Setenv("WEBSEARCH_ENGINES", "bing")
	// No CRAWL_URL and no render: reaching for the browser here would fail the test.
	t.Setenv("WEBSEARCH_RENDER", "")

	got := fetchEngine(context.Background(), bingEngine, "q", "")
	if got.outcome != answered || len(got.results) != 1 {
		t.Fatalf("answer = %+v, want the 203's results kept", got)
	}
	if got.browsed {
		t.Fatal("browsed = true, want no render for a status that already carried results")
	}
}

// An engine that was never REACHED says nothing about the parser, so it must not
// be reported as blind. Mixing the two would make every network blip look like
// selector rot and make the blind rate useless as a signal.
func TestUnreachableEngineIsFailedNotBlind(t *testing.T) {
	cacheReset()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()
	t.Setenv("WEBSEARCH_BING_URL", srv.URL)
	t.Setenv("WEBSEARCH_ENGINES", "bing")

	got := metaSearch(context.Background(), "q", "")
	if got.Engines[0].Outcome != string(failed) {
		t.Fatalf("outcome = %q, want %q for a non-200", got.Engines[0].Outcome, failed)
	}
}

// THE LOG LINE IS NARROW ON PURPOSE. An engine that returned nothing proves
// nothing on its own — the query may have no answers. An engine that returned
// nothing while a SIBLING answered the same query is proof the query has answers
// and that engine cannot see them. Only the second is worth reading.
func TestBlindIsLoggedOnlyWhenAnotherEngineAnswered(t *testing.T) {
	const good = `<html><body><li class="b_algo"><h2><a href="https://example.com/hit">Hit</a></h2><p>snippet</p></li></body></html>`

	t.Run("sibling answered — logged", func(t *testing.T) {
		cacheReset()
		buf := capture(t)
		engineServing(t, "WEBSEARCH_BING_URL", good)
		engineServing(t, "WEBSEARCH_DDG_URL", rotted)
		t.Setenv("WEBSEARCH_ENGINES", "bing,ddg")
		t.Setenv("WEBSEARCH_RENDER", "")

		metaSearch(context.Background(), "a query with real answers", "")
		if !strings.Contains(buf.String(), "ddg") {
			t.Fatalf("log = %q, want the blind engine named — bing answered, so ddg is provably broken", buf.String())
		}
	})

	t.Run("nothing answered — silent", func(t *testing.T) {
		cacheReset()
		buf := capture(t)
		engineServing(t, "WEBSEARCH_BING_URL", rotted)
		engineServing(t, "WEBSEARCH_DDG_URL", rotted)
		t.Setenv("WEBSEARCH_ENGINES", "bing,ddg")
		t.Setenv("WEBSEARCH_RENDER", "")

		metaSearch(context.Background(), "a query nobody has answered", "")
		if strings.Contains(buf.String(), "selector rot") {
			t.Fatalf("log = %q, want silence — with no engine answering there is no evidence the query HAS results", buf.String())
		}
	})
}

// The blend carries its own explanation. Three results with an engine blind is a
// different fact from three results with every engine answered, and a caller
// that cannot tell them apart is the caller that shipped a metasearch quietly
// running on one index.
func TestAnswerReportsEveryEngineAsked(t *testing.T) {
	cacheReset()
	const good = `<html><body><li class="b_algo"><h2><a href="https://example.com/hit">Hit</a></h2><p>s</p></li></body></html>`
	engineServing(t, "WEBSEARCH_BING_URL", good)
	engineServing(t, "WEBSEARCH_DDG_URL", rotted)
	t.Setenv("WEBSEARCH_ENGINES", "bing,ddg")
	t.Setenv("WEBSEARCH_RENDER", "")

	got := metaSearch(context.Background(), "q", "")
	if len(got.Engines) != 2 {
		t.Fatalf("engines = %+v, want one entry per engine asked", got.Engines)
	}
	by := map[string]webEngine{}
	for _, e := range got.Engines {
		by[e.Name] = e
	}
	if by[bingName].Outcome != string(answered) || by[bingName].Results != 1 {
		t.Fatalf("bing = %+v, want answered with 1 result", by[bingName])
	}
	if by[ddgName].Outcome != string(blind) || by[ddgName].Results != 0 {
		t.Fatalf("ddg = %+v, want blind with 0 results", by[ddgName])
	}
}
