package websearch

// brave.go — the one PAID engine, and the only one that costs us money.
//
// Every other engine here scrapes a public result page for free and is rate
// limited for it: measured from the cluster, DuckDuckGo answers about seven
// requests and then serves HTTP 202 with a challenge page for minutes. Brave
// sells an API with no such wall, and it answers better — for "rust tokio" it
// returns tokio.rs, github.com/tokio-rs/tokio and docs.rs, where the scraped
// engines have returned a fireworks retailer for "firecracker".
//
// SO IT IS OPT-IN, AND IT IS METERED. It is not in the default engine set: an
// engine that silently spends money the moment it is compiled in is a bill
// nobody agreed to. Naming it in WEBSEARCH_ENGINES is the agreement, and a
// missing key means it contributes nothing rather than erroring — the same rule
// every other engine follows.
//
// WHY THE PRICE IS NOT DECLARED AT THE EDGE. price.go's Consumes() makes GET
// free by construction — "a read spends nothing, so a read costs nothing" — and
// a search IS a read, so /v1/websearch/search can never carry a per-request edge
// price. price.go states the answer for exactly this case: "such a surface
// meters its own units downstream and declares Metered."
//
// THE CUSTOMER PAYS FOR THE ANSWER, NOT FOR OUR UPSTREAM CALL. The debit is one
// per search served with this engine enabled, taken in metaSearch — including
// when the cache answered and no Brave request was made. That is a deliberate
// pricing decision and not a cost pass-through: the price is what the answer is
// worth to the caller, and what we save by not re-asking is margin. Stating it
// here because a reader who assumed cost-recovery would "fix" the cache path
// into a discount and quietly change the product.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/hanzoai/cloud/internal/environ"
)

// braveKey is the subscription token. KMS-sourced, synced onto the cloud env as
// WEBSEARCH_BRAVE_KEY like every other secret this process reads — never a
// literal, and never a value in a manifest.
func braveKey() string { return strings.TrimSpace(os.Getenv("WEBSEARCH_BRAVE_KEY")) }

func braveURL() string {
	return environ.Or("WEBSEARCH_BRAVE_URL", "https://api.search.brave.com/res/v1/web/search")
}

// braveQuery is the request, in ONE place, because build() and the fetch must
// ask the identical question — a cache keyed on a URL the fetch does not send is
// a cache that answers for a different query.
func braveQuery(query, lang string) url.Values {
	v := url.Values{}
	v.Set("q", query)
	v.Set("count", "20")
	if lang != "" {
		v.Set("search_lang", lang)
	}
	return v
}

// collapse squeezes whitespace and bounds a snippet: an engine snippet is a few
// lines, and an unbounded one becomes an enormous "result".
func collapse(s string) string {
	out := strings.Join(strings.Fields(s), " ")
	const maxSnippet = 400
	if len(out) > maxSnippet {
		out = strings.TrimSpace(out[:maxSnippet]) + "…"
	}
	return out
}

// braveResponse is the subset of Brave's envelope we read. Its other fields
// (mixed, query, infobox, discussions) describe a page WE do not render, so
// reading them would be inventing a contract we do not serve.
type braveResponse struct {
	Web struct {
		Results []struct {
			URL         string `json:"url"`
			Title       string `json:"title"`
			Description string `json:"description"`
		} `json:"results"`
	} `json:"web"`
}

// braveEngine is JSON, not HTML — which is why it carries its own fetch instead
// of a `parse`. The engine struct's parse takes an *html.Node, and pretending a
// JSON API is a page in order to fit that shape would be the adapter this
// codebase keeps deleting. `fetch` is the client: an engine either parses HTML or
// fetches for itself, never both.
var braveEngine = engine{
	name: braveName,
	// The full request URL, query included — so the cache keys on the question
	// exactly as it does for every other engine, with no second key shape.
	build: func(query, lang string) string { return braveURL() + "?" + braveQuery(query, lang).Encode() },
	fetch: braveFetch,
}

func braveFetch(ctx context.Context, query, lang string) ([]webResult, error) {
	key := braveKey()
	if key == "" {
		// No key is not an error. It is this engine being unavailable, which is
		// the same state a challenged engine reaches, and the request is still
		// answered by whatever else is enabled.
		return nil, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, braveURL()+"?"+braveQuery(query, lang).Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", key)

	resp, err := searchClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errStatus(braveName, resp.StatusCode)
	}

	var out braveResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, err
	}

	res := make([]webResult, 0, len(out.Web.Results))
	for _, w := range out.Web.Results {
		if strings.TrimSpace(w.URL) == "" {
			continue
		}
		res = append(res, webResult{
			URL:     w.URL,
			Title:   strings.TrimSpace(w.Title),
			Content: collapse(stripTags(w.Description)),
			Engine:  braveName,
		})
	}
	return res, nil
}

// stripTags removes the <strong> emphasis Brave wraps query terms in. The
// snippet is text for a person or a model to read, and markup in it would be
// rendered literally by every consumer of the SearXNG envelope.
func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch {
		case r == '<':
			depth++
		case r == '>' && depth > 0:
			depth--
		case depth == 0:
			b.WriteRune(r)
		}
	}
	return b.String()
}
