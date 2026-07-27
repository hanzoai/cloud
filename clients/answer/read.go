package answer

// read.go — the READ stage: the loop's fourth stage, between rank and synthesize.
// Search gives a ~600-char snippet; a research-grade answer needs the PAGE. read()
// fetches the top sources through the ONE crawl (self-hosted Hanzo Crawl / Crawl4AI
// behind ai/object) and replaces each Source's Snippet with the fetched markdown.
//
// It enriches, it never re-identifies: URL/Title/Engine/Favicon are untouched, so
// the `sources` frame the client already rendered stays valid. It is also STRICTLY
// best-effort — a dead crawl service, a timeout, or an empty page degrades the
// answer back to the search snippet and never breaks the stream.

import (
	"context"
	"strings"
	"sync"

	aiobject "github.com/hanzoai/ai/object"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
)

const (
	// maxRead is the hard ceiling on pages fetched for one answer, whatever a mode
	// asks for — the read stage's cost bound inside the loop's 90s budget.
	maxRead = 6
	// maxPageText caps the fetched page text (runes) that replaces a snippet, so a
	// long page cannot blow the synthesis prompt's token budget.
	maxPageText = 6000
)

// Page is the crawl seam's value: the URL a page was fetched for and its
// LLM-ready markdown. The answer engine depends on THIS, never on the crawler's
// own result shape — which is what lets a test inject a fake crawl.
type Page struct {
	URL      string
	Markdown string
}

// crawl is the ONE crawl binding. Indirected through a var so tests substitute a
// fake and no test ever dials the crawl service.
var crawl = crawlPages

// read fetches the top sources and swaps each Snippet for the page's markdown.
// top<=0 (search/news) is a no-op — those modes ground on snippets and stay fast.
// Never returns an error: every failure path yields the sources unchanged.
func read(ctx context.Context, log luxlog.Logger, srcs []Source, top int) []Source {
	if top <= 0 || len(srcs) == 0 {
		return srcs
	}
	if top > maxRead {
		top = maxRead
	}
	if top > len(srcs) {
		top = len(srcs)
	}
	urls := make([]string, 0, top)
	for _, s := range srcs[:top] {
		urls = append(urls, s.URL)
	}

	text := make(map[string]string, top)
	for _, p := range crawl(ctx, log, urls) {
		if md := strings.TrimSpace(p.Markdown); md != "" {
			text[p.URL] = md
		}
	}
	for i := range srcs[:top] {
		if md, ok := text[srcs[i].URL]; ok {
			srcs[i].Snippet = clip(md, maxPageText)
		}
	}
	return srcs
}

// crawlPages runs the batch through the self-hosted Hanzo Crawl service and
// returns only the pages that actually came back. Failures yield nothing — the
// caller keeps the search snippets.
//
// The crawl runs on its own goroutine so ctx (the loop's 90s bound, or a client
// disconnect) cancels the WAIT even though the pinned ai/object.Crawl takes no
// ctx; the in-flight HTTP call still ends on its own 30s transport timeout. This
// collapses to a direct ctx-carrying call at the next ai bump — the ctx-threaded
// signature ships in hanzoai/ai on this same branch.
func crawlPages(ctx context.Context, log luxlog.Logger, urls []string) []Page {
	done := make(chan []Page, 1) // buffered: the goroutine never blocks after we give up
	var once sync.Once
	send := func(p []Page) { once.Do(func() { done <- p }) }

	// Contained, not bare. This goroutine parses pages we did not author, so it is
	// one of the likeliest places in the binary to panic — and an unrecovered panic
	// on a spawned goroutine kills the PROCESS, taking every other tenant with it
	// (middleware.Recover covers only the request goroutine). The inner defer is
	// registered first so it runs first: the caller is answered with "no pages"
	// before the panic is recovered and logged, otherwise the loop would sit out its
	// entire wall clock waiting for a result that is never coming.
	cloud.Go(log, "answer.crawl", []any{"urls", len(urls)}, func() {
		defer send(nil)
		res, err := aiobject.Crawl(urls)
		if err != nil {
			return
		}
		out := make([]Page, 0, len(res))
		for _, r := range res {
			if r.Success {
				out = append(out, Page{URL: r.URL, Markdown: r.Markdown})
			}
		}
		send(out)
	})
	select {
	case pages := <-done:
		return pages
	case <-ctx.Done():
		return nil
	}
}
