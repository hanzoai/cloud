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
// by normalized URL. WEBSEARCH_ENGINES selects them (default "bing", verified
// datacenter-tolerant from the cluster egress); adding one is a registry entry,
// not new plumbing. Any engine that fails or gets bot-challenged contributes zero
// and never fails the request — search degrades to fewer results, never to a 5xx.
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
	// maxResults caps the merged set — parity with a typical SearXNG first page and
	// plenty for the chat web_search agent tool.
	maxResults = 20

	// Engine names as constants: the parsers stamp result.Engine with them, so
	// they must NOT read <engine>.name (that closes an init cycle engine→parse→engine).
	bingName = "bing"
	ddgName  = "ddg"
)

// searchClient is dedicated to engine fetches: a tight timeout so one slow engine
// cannot stall the request, and the default redirect-following transport (engines
// 30x to their result pages). Separate from the crawl httpClient (45s).
var searchClient = &http.Client{Timeout: 12 * time.Second}

// searchResult is one web result in the SearXNG JSON contract the LibreChat
// searxng client decodes: {url,title,content,img_src?}. `engine` is additive
// (SearXNG includes it; the client ignores unknown fields).
type searchResult struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Content string `json:"content"`
	Engine  string `json:"engine,omitempty"`
}

// searchResponse is the SearXNG /search?format=json envelope. `results` is always
// a non-nil array so the client never decodes null.
type searchResponse struct {
	Query           string         `json:"query"`
	NumberOfResults int            `json:"number_of_results"`
	Results         []searchResult `json:"results"`
}

// engine is one keyless public web-search backend: build a request URL for a
// query, parse the returned HTML into results. Pure functions — unit-testable
// against fixture HTML with no network.
type engine struct {
	name  string
	build func(query, lang string) string
	parse func(root *html.Node) []searchResult
}

// ── engine endpoints (functions, not vars, so tests override via env) ────────

func bingURL() string { return envOr("WEBSEARCH_BING_URL", "https://www.bing.com/search") }
func ddgURL() string  { return envOr("WEBSEARCH_DDG_URL", "https://lite.duckduckgo.com/lite/") }

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

var engineByName = map[string]engine{
	bingEngine.name: bingEngine,
	ddgEngine.name:  ddgEngine,
}

// enabledEngines resolves WEBSEARCH_ENGINES (comma list) to the engine set,
// defaulting to bing. Unknown names are ignored; an empty result falls back to
// bing so search is never engine-less. DDG is opt-in (bot-challenged from some
// datacenter egress) but ships coded so `bing,ddg` needs no new plumbing.
func enabledEngines() []engine {
	spec := strings.TrimSpace(os.Getenv("WEBSEARCH_ENGINES"))
	if spec == "" {
		spec = bingEngine.name
	}
	var out []engine
	for _, name := range strings.Split(spec, ",") {
		if e, ok := engineByName[strings.ToLower(strings.TrimSpace(name))]; ok {
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		out = append(out, bingEngine)
	}
	return out
}

// metaSearch runs the enabled engines CONCURRENTLY and merges their results,
// deduped by normalized URL, capped at maxResults. Engine order is preserved
// (deterministic ranking: first engine's hits lead). A failing/challenged engine
// contributes nothing — the request never fails on its account.
func metaSearch(ctx context.Context, query, lang string) searchResponse {
	engs := enabledEngines()
	perEngine := make([][]searchResult, len(engs))

	var wg sync.WaitGroup
	for i := range engs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := fetchEngine(ctx, engs[i], query, lang)
			if err == nil {
				perEngine[i] = r
			}
		}(i)
	}
	wg.Wait()

	seen := make(map[string]bool)
	merged := make([]searchResult, 0, maxResults)
	for _, rs := range perEngine {
		for _, s := range rs {
			key := normalizeURL(s.URL)
			if key == "" || seen[key] {
				continue
			}
			seen[key] = true
			merged = append(merged, s)
			if len(merged) >= maxResults {
				return searchResponse{Query: query, NumberOfResults: len(merged), Results: merged}
			}
		}
	}
	return searchResponse{Query: query, NumberOfResults: len(merged), Results: merged}
}

// fetchEngine GETs one engine's result page with a realistic UA and parses it.
// Returns an error on transport/HTTP failure; a bot-challenge page parses to
// zero results (not an error), so it simply contributes nothing.
func fetchEngine(ctx context.Context, e engine, query, lang string) ([]searchResult, error) {
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
	if resp.StatusCode != http.StatusOK {
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

func parseBing(root *html.Node) []searchResult {
	var out []searchResult
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
		out = append(out, searchResult{URL: u, Title: title, Content: textContent(snip), Engine: bingName})
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

func parseDDG(root *html.Node) []searchResult {
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
	out := make([]searchResult, 0, len(links))
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
		out = append(out, searchResult{URL: u, Title: title, Content: content, Engine: ddgName})
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
