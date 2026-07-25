package ask

// relevance.go — the WEB grounding domain's mode registry (one parameterized code
// path for search/news/research/deep), its per-answer price policy, web-query
// construction, and the SERVER-SIDE source ranking. This is the "web/news/academic/
// research" domain of the advisor: unlike a figure domain (books), it grounds on
// live web sources rather than ledger figures. The ranking is the relevance fix:
// keyless meta-search returns broadly-matched pages, so the loop dedupes by
// URL+host and re-orders by query-term overlap before grounding — off-topic hits
// (which scored zero, e.g. an unrelated page for "Rich Hickey") sink.

import (
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/websearch"
)

// webMode is one web-grounding profile. plan toggles multi-query expansion;
// maxQueries and maxSources bound the loop; newsBias recency-biases the web query;
// system is the synthesis prompt; feeCents is the default per-answer price; models
// is the synthesis model fallback chain (primary first) — research/deep lead with a
// strong model for a quality report, search/news with a fast one.
type webMode struct {
	name       string
	plan       bool
	maxQueries int
	maxSources int
	newsBias   bool
	system     string
	feeCents   int64
	models     []string
}

// webModes is the registry. search/news are fast single-pass; research/deep plan
// sub-queries over a wider source set and synthesize a report. Deeper modes cost
// more (they do more work) — a legitimate product dimension, priced as policy.
//
// models is the per-mode synthesis chain: research/deep default to zen5 (a capable
// Hanzo model that streams FREE on the binary's M2M identity — a strong, always-
// reachable report writer), search/news to zen5-flash (the fast tier). zen5-flash
// backs research and zen5 backs search, so either mode still answers if its primary
// is down; the cloud-wide default is appended as a final backstop in synthModels.
var webModes = map[string]webMode{
	"search":   {name: "search", plan: false, maxQueries: 1, maxSources: 6, system: answerSystem, feeCents: 2, models: []string{"zen5-flash", "zen5"}},
	"news":     {name: "news", plan: false, maxQueries: 1, maxSources: 6, newsBias: true, system: answerSystem, feeCents: 2, models: []string{"zen5-flash", "zen5"}},
	"research": {name: "research", plan: true, maxQueries: 4, maxSources: 12, system: researchSystem, feeCents: 5, models: []string{"zen5", "zen5-flash"}},
	"deep":     {name: "deep", plan: true, maxQueries: 6, maxSources: 16, system: researchSystem, feeCents: 10, models: []string{"zen5", "zen5-flash"}},
}

// isWebMode reports whether a request mode selects the web grounding domain. An
// empty/unknown mode is NOT a web request — the advisor's figure path handles it,
// so the existing /v1/ask behavior is untouched when no web mode is set.
func isWebMode(name string) bool {
	_, ok := webModes[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

// resolveWebMode maps a request mode to a registry entry, defaulting to search.
// Only called after isWebMode has confirmed a web request.
func resolveWebMode(name string) webMode {
	if m, ok := webModes[strings.ToLower(strings.TrimSpace(name))]; ok {
		return m
	}
	return webModes["search"]
}

const (
	answerSystem = "You are Hanzo, an AI answer engine. Answer the question directly and accurately, grounded in the numbered web sources provided. " +
		"Lead with the answer; be concise, factual, and well structured (short paragraphs, bullets where they help). " +
		"Cite inline as Markdown links [source title](url) immediately after the claim each source supports, and cite generously. " +
		"Do NOT add a References or Sources section, footnote markers, or bare URLs — citations are inline links only. " +
		"If the sources conflict or are insufficient, say so plainly and answer from general knowledge while noting the uncertainty. Never fabricate facts or URLs."

	researchSystem = "You are Hanzo Deep Research. Synthesize a thorough, well-organized report answering the question from the numbered web sources. " +
		"Use clear section headings, compare sources, and surface the strongest evidence. " +
		"Cite inline as Markdown links [title](url) after each supported claim. " +
		"Do NOT add a trailing References section or bare URLs. Note gaps or disagreements between sources. Never fabricate facts or URLs."
)

// feeCents resolves the per-answer price in cents for a web mode, most specific
// first: CLOUD_ASK_FEE_CENTS_<MODE> → CLOUD_ASK_FEE_CENTS → the mode default. A
// value of 0 makes the mode free (and un-gated); a negative/invalid env value is
// ignored so a typo can never make a paid mode free. Mirrors ResourceFeeCents.
func feeCents(mode string, def int64) int64 {
	if v, ok := envCents("CLOUD_ASK_FEE_CENTS_" + strings.ToUpper(mode)); ok {
		return v
	}
	if v, ok := envCents("CLOUD_ASK_FEE_CENTS"); ok {
		return v
	}
	return def
}

func envCents(key string) (int64, bool) {
	s := strings.TrimSpace(os.Getenv(key))
	if s == "" {
		return 0, false
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// defaultSynthModel is the resilient synthesis anchor: a capable Hanzo model that
// streams FREE on the binary's M2M identity, so it stays reachable even when a paid
// or third-party model is throttled, out of balance, or its provider is down. It is
// the last-resort backstop if a mode/env/config chain ever resolves to nothing.
const defaultSynthModel = "zen5"

// synthModels resolves the SYNTHESIS model fallback chain for a web request, most
// specific first. The loop tries these in order until one returns a real answer, so
// a single model's outage advances to the next capable model instead of emitting a
// degraded "model unavailable" note:
//
//	1. caller's explicit model (in.Model) — honored outright, their one choice
//	2. per-mode env override CLOUD_ASK_MODEL_<MODE>, else global CLOUD_ASK_MODEL
//	3. the mode's capable defaults (research/deep → a strong model, search/news → a
//	   fast one) — every entry a catalog model that streams free on the M2M identity
//	4. the cloud-wide default (deps.AIDefaultModel) as a final backstop
//
// Blanks are dropped and duplicates collapsed (order preserved). An empty result —
// impossible in practice, the registry always seeds a mode default — falls back to
// defaultSynthModel so synthesis always has at least one model to try.
func synthModels(reqModel string, m webMode, def string) []string {
	if r := strings.TrimSpace(reqModel); r != "" {
		return []string{r}
	}
	chain := make([]string, 0, len(m.models)+2)
	if env := envModel("CLOUD_ASK_MODEL_"+strings.ToUpper(m.name), "CLOUD_ASK_MODEL"); env != "" {
		chain = append(chain, env)
	}
	chain = append(chain, m.models...)
	chain = append(chain, def)

	seen := make(map[string]bool, len(chain))
	out := make([]string, 0, len(chain))
	for _, s := range chain {
		if s = strings.TrimSpace(s); s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	if len(out) == 0 {
		return []string{defaultSynthModel}
	}
	return out
}

// envModel returns the first non-empty, trimmed environment value among keys.
func envModel(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// pickSystem lets a caller override the synthesis prompt; else the mode's default.
func pickSystem(req, def string) string {
	if s := strings.TrimSpace(req); s != "" {
		return s
	}
	return def
}

// clampPositive returns def when req<=0, else req bounded ABOVE by def — a caller
// may ask for fewer queries/sources but never more than the mode's ceiling, so the
// loop's cost stays bounded regardless of input.
func clampPositive(req, def int) int {
	if req <= 0 || req > def {
		return def
	}
	return req
}

// knownSourceHints are the @source tokens appended to the web query (SDK parity).
// "web" is the default (no hint). Unknown tokens are dropped so the query is not
// polluted by arbitrary caller input.
var knownSourceHints = map[string]bool{
	"news": true, "academic": true, "github": true, "reddit": true, "x": true,
}

// buildWebQuery composes the web-search string: the question, a recency bias for
// news mode, and any recognized @source hints appended as tokens.
func buildWebQuery(q string, m webMode, sources []string) string {
	wq := q
	if m.newsBias {
		wq = q + " latest news " + strconv.Itoa(time.Now().Year())
	}
	var hints []string
	for _, s := range sources {
		t := strings.ToLower(strings.TrimSpace(s))
		if knownSourceHints[t] {
			hints = append(hints, t)
		}
	}
	if len(hints) > 0 {
		wq = wq + " " + strings.Join(hints, " ")
	}
	return wq
}

// webSource is one web source backing a web-grounded answer — the @hanzo/ai
// SearchSource shape. Distinct from Fact (a books-style grounded figure): the web
// domain grounds on live sources, not ledger numbers.
type webSource struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	Engine  string `json:"engine,omitempty"`
	Favicon string `json:"favicon"`
}

// rankSources dedupes results (one per URL and one per host, preserving discovery
// order on ties) and orders them by relevance to the query, then caps at limit.
// Relevance = query-term overlap weighted toward the title plus a whole-phrase
// bonus, so a page that actually mentions the subject outranks a broad match.
func rankSources(query string, results []websearch.Result, limit int) []webSource {
	terms := queryTerms(query)
	phrase := strings.ToLower(strings.TrimSpace(query))

	type scored struct {
		src   webSource
		score int
		idx   int
	}
	seenURL := make(map[string]bool)
	seenHost := make(map[string]bool)
	list := make([]scored, 0, len(results))

	for i, r := range results {
		if r.URL == "" || seenURL[r.URL] {
			continue
		}
		host := hostOf(r.URL)
		if host == "" || seenHost[host] {
			continue
		}
		seenURL[r.URL] = true
		seenHost[host] = true
		list = append(list, scored{
			src: webSource{
				URL:     r.URL,
				Title:   orHost(r.Title, host),
				Snippet: clip(r.Content, 600),
				Engine:  r.Engine,
				Favicon: favicon(host),
			},
			score: relevanceScore(terms, phrase, r.Title, r.Content),
			idx:   i,
		})
	}

	sort.SliceStable(list, func(a, b int) bool {
		if list[a].score != list[b].score {
			return list[a].score > list[b].score // higher relevance first
		}
		return list[a].idx < list[b].idx // stable: preserve engine/discovery order on ties
	})

	out := make([]webSource, 0, limit)
	for _, s := range list {
		out = append(out, s.src)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// relevanceScore weights a title term hit 3× a content hit, and adds a whole-phrase
// bonus (title 5, content 2) so an exact-subject page rises to the top.
func relevanceScore(terms []string, phrase, title, content string) int {
	lt, lc := strings.ToLower(title), strings.ToLower(content)
	score := 0
	for _, t := range terms {
		if strings.Contains(lt, t) {
			score += 3
		}
		if strings.Contains(lc, t) {
			score++
		}
	}
	if phrase != "" {
		if strings.Contains(lt, phrase) {
			score += 5
		}
		if strings.Contains(lc, phrase) {
			score += 2
		}
	}
	return score
}

// queryTerms lowercases the query and returns its distinct content terms (≥2 chars,
// stopwords dropped) — the tokens relevance is scored against.
func queryTerms(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9')
	})
	seen := make(map[string]bool, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) < 2 || stopwords[f] || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

var stopwords = map[string]bool{
	"the": true, "and": true, "for": true, "are": true, "was": true, "who": true,
	"what": true, "why": true, "how": true, "when": true, "where": true, "which": true,
	"with": true, "from": true, "did": true, "does": true, "his": true, "her": true,
	"you": true, "your": true, "that": true, "this": true, "into": true, "about": true,
	"is": true, "of": true, "to": true, "in": true, "on": true, "at": true, "by": true,
	"or": true, "an": true, "as": true, "be": true, "it": true, "its": true, "has": true,
	"have": true, "had": true, "not": true, "but": true, "can": true, "will": true,
}

// hostOf returns the lowercased host of a URL, www-stripped, or "" if unparseable.
func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Host), "www.")
}

// favicon derives the Google s2 favicon for a host (matches the SDK's SearchSource).
func favicon(host string) string {
	if host == "" {
		return ""
	}
	return "https://www.google.com/s2/favicons?domain=" + host + "&sz=64"
}

func orHost(title, host string) string {
	if t := strings.TrimSpace(title); t != "" {
		return t
	}
	return host
}

// clip truncates s to at most n runes (no partial-rune corruption).
func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
