package answer

// rank.go — the ONE source value and the SERVER-SIDE ranking that orders it.
// Keyless meta-search returns broadly-matched pages, so the loop dedupes by
// URL+host and re-orders by query-term overlap before grounding — off-topic hits
// sink instead of polluting the answer.

import (
	"net/url"
	"sort"
	"strings"

	"github.com/hanzoai/cloud/clients/websearch"
)

// Source is one web source backing an answer — the @hanzo/ai SearchSource shape,
// field for field. It is the ONE source value in this package: search produces
// it, read() enriches only its Snippet (never its identity), synthesis grounds on
// it, and the wire emits it verbatim in the `sources` and `done` frames.
type Source struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	Engine  string `json:"engine,omitempty"`
	Favicon string `json:"favicon"`
}

// rank dedupes results (one per URL and one per host, preserving discovery order
// on ties) and orders them by relevance to the query, then caps at limit.
// Relevance = query-term overlap weighted toward the title plus a whole-phrase
// bonus, so a page that actually mentions the subject outranks a broad match.
func rank(query string, results []websearch.Result, limit int) []Source {
	terms := queryTerms(query)
	phrase := strings.ToLower(strings.TrimSpace(query))

	type scored struct {
		src   Source
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
			src: Source{
				URL:     r.URL,
				Title:   orHost(r.Title, host),
				Snippet: clip(r.Content, maxSnippet),
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

	out := make([]Source, 0, limit)
	for _, s := range list {
		out = append(out, s.src)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// maxSnippet caps a search-result snippet (runes). read() replaces it with far
// more page text for the modes that fetch (maxPageText).
const maxSnippet = 600

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
