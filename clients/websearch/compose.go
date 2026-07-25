package websearch

// compose.go — the in-process seam the answer engine (clients/answer) composes to
// use native meta-search as its GROUNDING source, one-way (answer imports
// websearch; websearch imports none of them). It is the SAME engines, concurrency,
// dedupe, and cap as GET /v1/websearch/search — not a second search stack and not
// an HTTP loopback back through the router. metaSearch is a pure function over the
// enabled engines, so this seam needs no mounted state and no Mount-order coupling.

import "context"

// Result is one web result exposed to in-process composers. It mirrors the
// SearXNG-compatible wire shape metaSearch produces (url/title/content/engine),
// re-exported so a composer never reaches into the package-private searchResult.
type Result struct {
	URL     string
	Title   string
	Content string
	Engine  string
}

// Search runs native meta-search in-process for query (lang optional, BCP-47-ish)
// and returns the merged, URL-deduped results — the ONE grounding seam the answer
// engine calls per sub-query. It never errors: a failing or bot-challenged engine
// contributes zero results (search degrades to fewer sources, never to a 5xx), so
// the caller always gets a usable slice and decides how to rank/cap it.
func Search(ctx context.Context, query, lang string) []Result {
	resp := metaSearch(ctx, query, lang)
	out := make([]Result, 0, len(resp.Results))
	for _, r := range resp.Results {
		out = append(out, Result{URL: r.URL, Title: r.Title, Content: r.Content, Engine: r.Engine})
	}
	return out
}
