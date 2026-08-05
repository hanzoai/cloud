package answer

// rank.go — the ONE source value and the SERVER-SIDE ranking that orders it.
// Keyless meta-search returns broadly-matched pages, so the loop dedupes by
// URL+host and re-orders by query-term overlap before grounding — off-topic hits
// sink instead of polluting the answer.

import (
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/hanzoai/cloud/apps/websearch"
)

// Source is one web source backing an answer — the @hanzo/ai SearchSource shape,
// field for field. It is the ONE source value in this package: search produces
// it, read() fills its Text, synthesis grounds on it, and the wire emits it
// verbatim in the `sources` and `done` frames.
//
// SNIPPET IS WHAT THE CLIENT SHOWS; TEXT IS WHAT THE MODEL READS. Snippet is
// always the search engine's ~600-rune summary. Text is the fetched page —
// thousands of runes of markup we did not author, per source, re-ranked every
// round — and `json:"-"` is what keeps it off the wire: a rendered snippet is
// somebody else's text either way, but a bounded amount of it, and a survey that
// shipped its whole corpus in every snapshot would send a megabyte of duplicate
// SSE per answer. read() may touch no other field: the `sources` frame the client
// already rendered has to stay valid.
type Source struct {
	URL     string `json:"url"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	Engine  string `json:"engine,omitempty"`
	Favicon string `json:"favicon"`
	Text    string `json:"-"`
}

// rank dedupes results (one per URL, at most hostCap per host, preserving
// discovery order on ties) and orders them by relevance to the query, then caps
// at limit. Relevance = query-term overlap weighted toward the title plus a
// whole-phrase bonus, so a page that actually mentions the subject outranks a
// broad match.
//
// hostCap is a mode value, not a constant: one page per host is right for a
// six-source answer, where breadth IS the value, and wrong for research, where
// three pages from an authoritative domain are the point. hostCap<=1 reproduces
// the one-per-host set exactly.
func rank(query string, results []websearch.Result, limit, hostCap int) []Source {
	terms := queryTerms(query)
	phrase := strings.ToLower(strings.TrimSpace(query))
	if hostCap < 1 {
		hostCap = 1
	}

	type scored struct {
		src   Source
		score int
		idx   int
	}
	seenURL := make(map[string]bool)
	hostCount := make(map[string]int)
	list := make([]scored, 0, len(results))

	for i, r := range results {
		if r.URL == "" || seenURL[r.URL] {
			continue
		}
		host := hostOf(r.URL)
		if host == "" || hostCount[host] >= hostCap {
			continue
		}
		seenURL[r.URL] = true
		hostCount[host]++
		list = append(list, scored{
			src: Source{
				URL:     r.URL,
				Title:   orHost(cleanTitle(r.Title), host),
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

// titleNoise matches the bracketed and parenthesised furniture search engines
// staple onto a title — "[PDF]", "(Official Site)", "[2024 Update]". It is
// citation noise: the link text should read as the document's name.
var titleNoise = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)`)

// cleanTitle strips that noise and collapses the whitespace it leaves behind.
//
// A title that is ENTIRELY bracketed is kept as-is: stripping it would leave the
// empty string and the source would be cited by its bare hostname instead of its
// name. Losing the title of every wholly-parenthesised page is a worse outcome
// than keeping its parentheses.
func cleanTitle(s string) string {
	stripped := strings.Join(strings.Fields(titleNoise.ReplaceAllString(s, " ")), " ")
	if stripped == "" {
		return s
	}
	return stripped
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
