// Native Go meta-search — the SEARCH half of /v1/websearch, replacing the
// reverse proxy to the (retired) SearXNG pod.
//
// Rationale (HIP-0106 preserved): web search stays Hanzo-native with NO external
// search SaaS — never Brave/Serper/Tavily/Jina/Cohere and never a paid search-API
// key. SearXNG earned its keep as keyless meta-search over public engines; this
// file does the SAME job in-process in Go, so the stock Python metasearch pod is
// gone (one fewer non-Go dependency) and search no longer 502s when it is down.
//
// It queries keyless public engines directly over native Go HTTP, parses their
// HTML with x/net/html, and returns the SearXNG JSON contract the LibreChat
// searxng client decodes verbatim: {results:[{url,title,content,...}]}. So the
// chat server needs NO change — searxngInstanceUrl already points at this surface.
//
// Composable by construction: an engine is {name, build(query)→URL, parse(HTML)
// →results}. metaSearch runs the ENABLED engines concurrently and merges+dedupes
// by normalized URL. WEBSEARCH_ENGINES selects them; unset means defaultEngines,
// which is every engine measured to survive the cluster egress (bing + mojeek).
// Adding one is a registry entry, not new plumbing. Any engine that fails or is
// bot-challenged contributes zero and never fails the request — search degrades
// to fewer results, never to a 5xx.

package websearch

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

const (
	// A realistic desktop UA. Keyless engines serve datacenter IPs a bot-challenge
	// page (not results) when the UA looks automated; this one gets real results.
	browserUA = "Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0"
	// maxResults caps the merged set. Bing's first page is 10 and Mojeek is asked
	// for 20, so the cap is what the two of them can actually reach rather than a
	// number one engine could never fill.
	maxResults = 30

	// Engine names as constants: the parsers stamp result.Engine with them, so
	// they must NOT read <engine>.name (that closes an init cycle engine→parse→engine).
	bingName   = "bing"
	ddgName    = "ddg"
	mojeekName = "mojeek"
	braveName  = "brave"

	// mojeekCount is how many hits Mojeek is asked for. It honours `t` exactly
	// (measured: t=20 → 20 results, t=30 → 30), so this is the one knob that
	// raises the answer's size without adding an engine.
	mojeekCount = "20"
)

// searchClient is dedicated to engine fetches: a tight timeout so one slow engine
// cannot stall the request, and the default redirect-following transport (engines
// 30x to their result pages). Separate from the crawl httpClient (45s).
var searchClient = &http.Client{Timeout: 12 * time.Second}

// webResult is one web result in the SearXNG JSON contract the LibreChat
// searxng client decodes: {url,title,content,img_src?}. `engine` is additive
// (SearXNG includes it; the client ignores unknown fields).
//
// It is named for its product rather than searchResult, and webSearchResults
// likewise: the schema namespace is FLAT across the whole fleet and both are
// PUBLISHED now that POST /v1/websearch is a typed op, so the generic spelling
// would have claimed two of the most collidable names in the API for one
// subsystem. Only the Go names moved; every json tag is the one SearXNG's
// contract froze.
type webResult struct {
	// URL is the page's address, as the engine reported it.
	URL string `json:"url"`
	// Title is the page's title.
	Title string `json:"title"`
	// Content is the ENGINE's snippet — the few lines shown under the title, not
	// the page's text. Read the page itself with POST /v1/crawl.
	Content string `json:"content"`
	// Engine names the backend that found this hit, so one engine's view of a
	// query can be told from another's.
	Engine string `json:"engine,omitempty"`
}

// webEngine is what ONE engine did on this query: what it is called, how its
// turn ended, and how many hits it contributed before the merge.
//
// It is published so a thin answer carries its own explanation. Without it, an
// engine that has stopped working shows up only as fewer results, and the caller
// cannot tell "the web is quiet on this" from "half our indexes are blind" — the
// exact ambiguity that let DuckDuckGo sit in the default set contributing zero.
// Three results with `ddg blind` is a different fact from three results with
// every engine answered, and the caller deserves to see which one it got.
type webEngine struct {
	// Name is the engine, matching the `engine` stamped on each result.
	Name string `json:"name"`
	// Outcome is "answered", "blind" or "failed" — see outcome.go. "blind" means
	// the page came back and no results could be read out of it.
	Outcome string `json:"outcome"`
	// Results is how many hits this engine contributed, before the merge
	// deduplicated them against the others.
	Results int `json:"results"`
}

// webSearchResults is the SearXNG /search?format=json envelope. `results` is always
// a non-nil array so the client never decodes null.
type webSearchResults struct {
	// Query is the query that ran, echoed back.
	Query string `json:"query"`
	// NumberOfResults is len(results) — what this answer carries, never an
	// estimate of what the web holds.
	NumberOfResults int `json:"number_of_results"`
	// Results are the merged hits, deduplicated by normalised URL and capped at
	// 30. Always an array and never null: no hits is an ANSWER, not a fault.
	Results []webResult `json:"results"`
	// Engines is one entry per engine asked, in the order they were asked. It is
	// ADDITIVE to the SearXNG contract, which the LibreChat client ignores as an
	// unknown field exactly as it ignores `engine` on a result.
	Engines []webEngine `json:"engines,omitempty"`
}

// engine is one keyless public web-search backend: build a request URL for a
// query, parse the returned HTML into results. Pure functions — unit-testable
// against fixture HTML with no network.
//
// `fetch` is the one variation, and it is a seam rather than an adapter: an
// engine that is a JSON API instead of a page answers for itself. Brave sells
// one, and reshaping JSON into an *html.Node so it could reach `parse` is
// exactly the shim this package keeps deleting. When fetch is set, parse is
// unused; build still names the full request so the cache keys on the question
// like every other engine.
type engine struct {
	name  string
	build func(query, lang string) string
	parse func(root *html.Node) []webResult
	fetch func(ctx context.Context, query, lang string) ([]webResult, error)
}

// errStatus is the one shape an engine reports a refusing endpoint with, so a
// 202 challenge and a 429 quota read the same to whatever counts them.
func errStatus(engine string, code int) error {
	return fmt.Errorf("%s: http %d", engine, code)
}

// ── engine endpoints (functions, not vars, so tests override via env) ────────

func bingURL() string   { return envOr("WEBSEARCH_BING_URL", "https://www.bing.com/search") }
func ddgURL() string    { return envOr("WEBSEARCH_DDG_URL", "https://lite.duckduckgo.com/lite/") }
func mojeekURL() string { return envOr("WEBSEARCH_MOJEEK_URL", "https://www.mojeek.com/search") }

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

var bingEngine = engine{
	name: bingName,
	build: func(query, lang string) string {
		v := url.Values{}
		v.Set("q", query)
		if lang != "" {
			v.Set("setlang", lang)
		}
		return bingURL() + "?" + v.Encode()
	},
	parse: parseBing,
}

var ddgEngine = engine{
	name: ddgName,
	build: func(query, lang string) string {
		v := url.Values{}
		v.Set("q", query)
		if lang != "" {
			v.Set("kl", lang)
		}
		return ddgURL() + "?" + v.Encode()
	},
	parse: parseDDG,
}

// mojeek is an INDEPENDENT crawler rather than a front end onto someone else's
// index, so its hits are genuinely additive to Bing's instead of the same ten
// pages in a different order. Two properties earned it the default slot, both
// measured from cluster egress rather than assumed:
//
//   - it serves a datacenter IP real results, where DuckDuckGo serves the
//     anomaly page on both of its endpoints;
//   - it honours `site:`, and Bing does not. A site:x.com query answers 10 on
//     Mojeek and 0 on Bing, which makes this the engine that carries every
//     scoped search — X, GitHub, Reddit — and not merely a second opinion.
var mojeekEngine = engine{
	name: mojeekName,
	build: func(query, lang string) string {
		v := url.Values{}
		v.Set("q", query)
		v.Set("t", mojeekCount)
		if lang != "" {
			v.Set("lb", lang)
		}
		return mojeekURL() + "?" + v.Encode()
	},
	parse: parseMojeek,
	// The API when a key is held, the scraped page when not — see mojeek_api.go.
	fetch: mojeekAPIFetch,
}

var engineByName = map[string]engine{
	bingEngine.name:   bingEngine,
	ddgEngine.name:    ddgEngine,
	mojeekEngine.name: mojeekEngine,
	braveEngine.name:  braveEngine,
}

// defaultEngines is what a deployment that configures nothing searches: every
// engine measured to answer from datacenter egress. A LIST rather than one name
// because production once ran with WEBSEARCH_ENGINES unset, which made this
// default the whole engine set, and it was Bing alone — one index, ten results,
// no `site:`.
//
// DDG is here even though it is served a captcha over static HTTP, because
// render.go escalates and the browser reads it (measured: 0 static, 10 rendered,
// same URL, same second). It earns the slot on the rendered number. If the
// escalation is off, DDG is BLIND rather than quietly absent — outcome.go says
// so in the answer and in the metric — which is the property that makes putting
// a browser-dependent engine in the default set honest rather than optimistic.
//
// Bing is the weakest of the three and stays because it is the broadest. It has
// no zero state at all: asked three distinct nonsense strings it returned ten
// results each time (Edmonton property tax, Bastille Day, Microsoft support),
// and for a fourth, pornography. It never abstains, so its hits carry no
// evidence of relevance on their own. That is precisely what rank.go's agreement
// scoring is for — a Bing hit no other index found ranks below one two of them
// agree on — and it is why removing Bing is not obviously wrong, only untested.
var defaultEngines = []engine{bingEngine, ddgEngine, mojeekEngine}

// enabledEngines resolves WEBSEARCH_ENGINES (comma list) to the engine set,
// defaulting to [defaultEngines]. Unknown names are ignored, and a spec that
// names none of the known engines falls back to the same default so search is
// never engine-less.
func enabledEngines() []engine {
	spec := strings.TrimSpace(os.Getenv("WEBSEARCH_ENGINES"))
	if spec == "" {
		return defaultEngines
	}
	var out []engine
	for _, name := range strings.Split(spec, ",") {
		if e, ok := engineByName[strings.ToLower(strings.TrimSpace(name))]; ok {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return defaultEngines
	}
	return out
}

// metaSearch runs the enabled engines CONCURRENTLY and merges their results,
// deduped by normalized URL, capped at maxResults. A failing or challenged engine
// contributes nothing — the request never fails on its account.
//
// The merge is ranked by AGREEMENT (rank.go), not by the order engines were
// named. It used to be the latter, and that made WEBSEARCH_ENGINES an accidental
// relevance knob: with `bing,ddg`, bing's three irrelevant hits for "post quantum
// cryptography lattice" opened the page while ddg's correct ones were pushed
// below them. Which engine is listed first is a configuration fact and was never
// evidence about a result.
func metaSearch(ctx context.Context, query, lang string) webSearchResults {
	engs := enabledEngines()
	answers := make([]answer, len(engs))

	var wg sync.WaitGroup
	for i := range engs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			answers[i] = fetchEngine(ctx, engs[i], query, lang)
		}(i)
	}
	wg.Wait()

	// Say what happened before saying what was found. An engine that went blind
	// is a fact about this answer, and reporting it here — once, where every
	// caller of metaSearch passes — is what stops a broken engine from being
	// visible only as a slightly shorter page. See outcome.go.
	report(ctx, query, answers)

	perEngine := make([][]webResult, len(answers))
	engines := make([]webEngine, len(answers))
	for i, a := range answers {
		perEngine[i] = a.results
		engines[i] = webEngine{Name: a.engine, Outcome: string(a.outcome), Results: len(a.results)}
	}
	merged := rankMerged(perEngine, maxResults)
	return webSearchResults{
		Query:           query,
		NumberOfResults: len(merged),
		Results:         merged,
		Engines:         engines,
	}
}

// fetchEngine asks one engine and says how the asking went.
//
// It answers from the cache when it can, fetches statically when it cannot, and
// escalates to a real browser when the static fetch parsed to NOTHING — which is
// what a bot challenge looks like here, since a challenge is a 200 whose markup
// holds no results. The three live in that order because that is their cost
// order: remembered, then one GET, then a browser render. See cache.go and
// render.go for why each is correctness rather than speed.
//
// It returns an `answer` rather than ([]webResult, error) because zero results
// is NOT the same fact as "nothing to report", and the pair could not tell them
// apart: a challenged engine and a query with no matches were both (nil, nil).
// outcome.go has the measurements that make the difference concrete.
//
// Note what `blind` means AFTER the escalation ran: we rendered the page in a
// real browser and still read zero results out of it. That is the strongest
// evidence of selector rot the system can produce, and it is exactly the signal
// that was missing when Brave was dropped for having unreadable markup.
func fetchEngine(ctx context.Context, e engine, query, lang string) answer {
	url := e.build(query, lang)
	if hit, ok := cacheGet(url); ok {
		return answer{engine: e.name, results: hit, outcome: answered}
	}

	// A JSON engine answers for itself. It is cached like the others — a PAID
	// API is the one we least want to ask twice for the same question — and it
	// reports the same outcomes, so a quota refusal reads as `blind` rather than
	// as an engine that had nothing to say.
	// A JSON engine answers for itself, and is cached like any other — a PAID API
	// is the one we least want to ask twice for the same question.
	//
	// (nil, nil) is an engine saying "NOT BY THIS DOOR" rather than "nothing is
	// there": mojeek without a key. When the engine also has a parser, the static
	// path below runs and the scraped page answers. That is a fallback chain, not
	// a second engine — one name, one registry entry, and the credential decides
	// which door it knocks on. Returning `blind` here instead cost mojeek its
	// keyless answer entirely, which is the free tier of this product.
	if e.fetch != nil {
		out, err := e.fetch(ctx, query, lang)
		if len(out) > 0 {
			cachePut(url, out)
			return answer{engine: e.name, results: out, outcome: answered}
		}
		if err != nil {
			return answer{engine: e.name, outcome: failed}
		}
		if e.parse == nil {
			return answer{engine: e.name, outcome: blind}
		}
	}

	out, err := fetchEngineStatic(ctx, e, query, lang)
	if len(out) > 0 {
		cachePut(url, out)
		return answer{engine: e.name, results: out, outcome: answered}
	}

	// NOTHING READABLE CAME BACK, and there is ONE remedy for that whichever way
	// it happened: render the page in a real browser and read it again.
	//
	// This used to be two rules, and the seam between them cost us DuckDuckGo
	// entirely. Escalation ran only when a 200 parsed to zero, so a bad status
	// short-circuited to "failed" and never reached the browser. DDG's challenge
	// is served as HTTP 202 — measured, three times over, 14,180 bytes of "Select
	// all squares containing a duck" under a 2xx — so the one engine the browser
	// was deployed to rescue was the one engine that could never reach it.
	//
	// A status is a fact about the fetch, not about whether a browser can read
	// the page. Keeping it as a separate rule was a distinction the remedy does
	// not have.
	//
	// A REFUSAL is worth a render for a reason that is not obvious: the browser
	// runs in a different pod on a different node, so it leaves the cluster from a
	// different address than this process does. An engine rate-limiting one of our
	// egress IPs has not necessarily rate-limited the other. What it will NOT
	// rescue is a refusal aimed at the browser's own address — asked to render a
	// URL that had just answered it 403, Crawl returned 0 bytes. So this can
	// recover an engine and can also spend ~1.1s learning nothing, which is
	// affordable because the engines run concurrently and renderTimeout bounds it.
	rendered, browsed := renderedResults(ctx, e, query, lang)
	if len(rendered) > 0 {
		cachePut(url, rendered)
		return answer{engine: e.name, results: rendered, outcome: answered, browsed: browsed}
	}
	if err != nil {
		// Never got a readable page at all, so this says nothing about the parser.
		return answer{engine: e.name, outcome: failed, browsed: browsed, err: err}
	}
	return answer{engine: e.name, outcome: blind, browsed: browsed}
}

func fetchEngineStatic(ctx context.Context, e engine, query, lang string) ([]webResult, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.build(query, lang), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")

	resp, err := searchClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// ANY 2xx is an answer worth parsing, not just 200. DuckDuckGo serves its bot
	// challenge as 202, and an engine is free to answer 203 or 206 as well;
	// singling out 200 discarded bodies we had already paid to fetch and turned a
	// readable page into a transport error.
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("%s: http %d", e.name, resp.StatusCode)
	}
	root, err := html.Parse(io.LimitReader(resp.Body, 4<<20)) // 4 MiB cap
	if err != nil {
		return nil, err
	}
	return e.parse(root), nil
}

// ── Bing: parse <li class="b_algo"> blocks ───────────────────────────────────
// Title is <h2><a href="…ck/a?…u=a1<base64url>">Title</a>; the real destination
// is base64url-encoded in the ck/a redirect's u= param (a1 prefix). <cite> holds
// a display URL as a fallback. Snippet is the caption <p>.

func parseBing(root *html.Node) []webResult {
	var out []webResult
	forEach(root, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "li" || !hasClass(n, "b_algo") {
			return
		}
		h2 := findFirst(n, func(x *html.Node) bool { return x.Type == html.ElementNode && x.Data == "h2" })
		if h2 == nil {
			return
		}
		a := findFirst(h2, func(x *html.Node) bool {
			return x.Type == html.ElementNode && x.Data == "a" && attr(x, "href") != ""
		})
		if a == nil {
			return
		}
		title := textContent(a)
		u := bingRealURL(attr(a, "href"))
		if u == "" {
			if cite := findFirst(n, func(x *html.Node) bool { return x.Type == html.ElementNode && x.Data == "cite" }); cite != nil {
				u = normalizeCiteURL(textContent(cite))
			}
		}
		if u == "" || title == "" {
			return
		}
		snip := findFirst(n, func(x *html.Node) bool {
			return x.Type == html.ElementNode && x.Data == "p" && hasClassPrefix(x, "b_lineclamp")
		})
		if snip == nil {
			snip = findFirst(n, func(x *html.Node) bool { return x.Type == html.ElementNode && x.Data == "p" })
		}
		out = append(out, webResult{URL: u, Title: title, Content: textContent(snip), Engine: bingName})
	})
	return out
}

// bingRealURL unwraps Bing's /ck/a click-tracking redirect to the real target
// (u=a1<base64url>). A non-ck/a absolute http(s) href is returned as-is. Anything
// else (relative/undecodable) returns "" so the caller falls back to <cite>.
func bingRealURL(href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if strings.Contains(u.Host, "bing.com") && strings.HasPrefix(u.Path, "/ck/a") {
		raw := u.Query().Get("u")
		if !strings.HasPrefix(raw, "a1") {
			return ""
		}
		enc := raw[2:]
		if dec, err := base64.RawURLEncoding.DecodeString(enc); err == nil {
			return string(dec)
		}
		if dec, err := base64.URLEncoding.DecodeString(enc); err == nil {
			return string(dec)
		}
		return ""
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		return href
	}
	return ""
}

// normalizeCiteURL turns a Bing display cite ("https://a.com › wiki › X") into a
// usable absolute URL — the host is the leading token; the " › " path segments
// are lossy so we keep the origin, which is enough for the agent to fetch/scrape.
func normalizeCiteURL(cite string) string {
	f := strings.Fields(cite)
	if len(f) == 0 {
		return ""
	}
	first := f[0]
	if strings.HasPrefix(first, "http://") || strings.HasPrefix(first, "https://") {
		if u, err := url.Parse(first); err == nil && u.Host != "" {
			return u.Scheme + "://" + u.Host
		}
	}
	return ""
}

// ── DuckDuckGo Lite: <a class="result-link" href="…"> + <td class="result-snippet"> ─
// Best-effort: the datacenter egress is often served the anomaly (bot-challenge)
// page, which has no result-link nodes → parseDDG returns nil and DDG contributes
// nothing. Result hrefs may be direct or //duckduckgo.com/l/?uddg=<target>.

func parseDDG(root *html.Node) []webResult {
	var links, snips []*html.Node
	forEach(root, func(n *html.Node) {
		if n.Type != html.ElementNode {
			return
		}
		if n.Data == "a" && hasClass(n, "result-link") {
			links = append(links, n)
		}
		if hasClass(n, "result-snippet") {
			snips = append(snips, n)
		}
	})
	out := make([]webResult, 0, len(links))
	for i, a := range links {
		u := ddgRealURL(attr(a, "href"))
		title := textContent(a)
		if u == "" || title == "" {
			continue
		}
		content := ""
		if i < len(snips) {
			content = textContent(snips[i])
		}
		out = append(out, webResult{URL: u, Title: title, Content: content, Engine: ddgName})
	}
	return out
}

func ddgRealURL(href string) string {
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if strings.Contains(u.Host, "duckduckgo.com") {
		if t := u.Query().Get("uddg"); t != "" { // url.Values already percent-decodes
			return t
		}
		return "" // internal DDG link (settings, etc.) — skip
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		return href
	}
	return ""
}

// ── Mojeek: one <li> per hit, <a class="title"> + <p class="s"> ──────────────
// Mojeek links straight at the destination — there is no click-tracking redirect
// to unwrap, which is why this parser has no counterpart to bingRealURL. The
// result classes are semantic (title, s) rather than build-hashed, so they
// survive a redeploy; that is what makes this engine parseable at all where
// Brave, whose classes are Svelte hashes like `svelte-1rq4ngz`, is not.

func parseMojeek(root *html.Node) []webResult {
	var out []webResult
	forEach(root, func(n *html.Node) {
		if n.Type != html.ElementNode || n.Data != "li" {
			return
		}
		a := findFirst(n, func(x *html.Node) bool {
			return x.Type == html.ElementNode && x.Data == "a" && hasClass(x, "title")
		})
		if a == nil {
			return
		}
		u, title := attr(a, "href"), textContent(a)
		if title == "" || !strings.HasPrefix(u, "http") {
			return
		}
		snip := findFirst(n, func(x *html.Node) bool {
			return x.Type == html.ElementNode && x.Data == "p" && hasClass(x, "s")
		})
		out = append(out, webResult{URL: u, Title: title, Content: textContent(snip), Engine: mojeekName})
	})
	return out
}

// normalizeURL is the dedupe key: lowercased host + path (trailing slash and
// fragment dropped, query kept — distinct queries are distinct results).
func normalizeURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.TrimSpace(raw)
	}
	key := strings.ToLower(u.Host) + strings.TrimRight(u.Path, "/")
	if u.RawQuery != "" {
		key += "?" + u.RawQuery
	}
	return key
}

// ── x/net/html DOM helpers (shared by every engine parser) ───────────────────

func forEach(n *html.Node, fn func(*html.Node)) {
	fn(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		forEach(c, fn)
	}
}

func findFirst(n *html.Node, pred func(*html.Node) bool) *html.Node {
	var found *html.Node
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if found != nil {
			return
		}
		if pred(x) {
			found = x
			return
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return found
}

func attr(n *html.Node, key string) string {
	if n == nil {
		return ""
	}
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

func hasClassPrefix(n *html.Node, prefix string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

// textContent is the collapsed visible text of a node subtree (nil-safe).
func textContent(n *html.Node) string {
	if n == nil {
		return ""
	}
	var sb strings.Builder
	forEach(n, func(x *html.Node) {
		if x.Type == html.TextNode {
			sb.WriteString(x.Data)
			sb.WriteByte(' ')
		}
	})
	return strings.Join(strings.Fields(sb.String()), " ")
}
