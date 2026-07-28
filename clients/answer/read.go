package answer

// read.go — the READ stage: the loop's fourth stage, between rank and synthesize.
// Search gives a ~600-char snippet; a research-grade answer needs the PAGE. read()
// fetches the top sources through the ONE crawl (clients/crawl, in this binary) and
// replaces each Source's Snippet with the fetched markdown.
//
// It enriches, it never re-identifies: URL/Title/Engine/Favicon are untouched, so
// the `sources` frame the client already rendered stays valid. It is also STRICTLY
// best-effort — a dead crawl service, a timeout, or an empty page degrades the
// answer back to the search snippet and never breaks the stream.

import (
	"context"
	"strings"

	luxlog "github.com/luxfi/log"

	crawlpkg "github.com/hanzoai/cloud/clients/crawl"
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
func read(ctx context.Context, log luxlog.Logger, scope crawlpkg.Scope, srcs []Source, top int) []Source {
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
	for _, p := range crawl(ctx, log, scope, urls) {
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

// crawlPages fetches each URL through the in-binary crawl and returns the pages
// that came back. Failures yield nothing for that URL — the caller keeps the
// search snippet.
//
// This is the collapse the previous comment promised, and it went further than a
// ctx-threaded signature: there is no crawl SERVICE left to call. It used to reach
// ai/object.Crawl, which dialled crawl.hanzo.svc.cluster.local:11235 — a name that
// does not resolve. Every research and deep answer therefore read zero pages and
// silently degraded to snippets, which is the failure mode this stage is explicitly
// allowed to have, so nothing ever looked broken.
//
// Because clients/crawl.Fetch takes a ctx, the machinery that used to be needed
// here is gone rather than rebound: one goroutine to make an uncancellable batch
// call cancellable, a sync.Once to keep the abandoned goroutine from blocking on
// send. Fetch honours ctx directly, so the loop's 90s bound and a client
// disconnect now reach the socket instead of merely ending the wait while the
// request ran on unattended.
//
// Per URL, not per batch: one slow page no longer decides when the others arrive,
// and a page that panics the HTML parser costs its own slot instead of the set. The
// collector owns `out` alone — the workers only send — so results are assembled
// without a mutex and a straggler that lands after ctx expires writes to a buffered
// channel nobody reads, which is safe rather than a race.
//
// Pages are read through crawlpkg.Read, so a research loop that revisits a URL —
// across a re-ask, a follow-up, or a later re-index — pays the network once and
// reads the corpus after. The scope is the DATA org, never the payer: the corpus
// is the tenant's data, and filing it under whoever happened to fund the answer
// would put one org's pages in another's prefix.
//
// Page.URL is the url we ASKED for, never Read's post-redirect final url. read()
// keys its map by this and matches it against the Source's own URL; handing back
// the redirected address would silently fail that match on exactly the pages that
// redirect, and the answer would drop them while looking like it read them.
func crawlPages(ctx context.Context, log luxlog.Logger, scope crawlpkg.Scope, urls []string) []Page {
	type slot struct {
		i    int
		page Page
	}
	// Buffered to exactly the worker count: every worker sends once and none can
	// block, so a straggler that finishes after ctx expired exits cleanly instead of
	// leaking on an unread channel.
	ch := make(chan slot, len(urls))

	for i, u := range urls {
		go func(i int, u string) {
			p := Page{URL: u}
			// One defer, ordered deliberately: recover FIRST, then send. This worker
			// parses markup we did not author, the likeliest place in the binary to
			// panic, and an unrecovered panic on a spawned goroutine kills the
			// PROCESS with every tenant on it — middleware.Recover only ever covered
			// the request goroutine. Sending inside the same defer guarantees the
			// collector is answered on the panic path too; a worker that died
			// silently would cost the loop its whole wall clock waiting for a result
			// that is not coming.
			defer func() {
				// log is nil on some call paths (and in tests) — a nil deref inside
				// the recover defer would turn a contained panic back into a process
				// kill, at the worst possible moment.
				if r := recover(); r != nil && log != nil {
					log.Error("crawl worker panicked (recovered; answer degrades to snippet)", "url", u, "panic", r)
				}
				ch <- slot{i, p}
			}()
			got, err := crawlpkg.Read(ctx, scope, u)
			if err != nil || got == nil {
				return // p keeps an empty Markdown: this source stays on its snippet
			}
			p.Markdown = got.Markdown
		}(i, u)
	}

	// The collector is the ONLY writer of slots, so ranked order survives without a
	// mutex over shared state.
	slots := make([]Page, len(urls))
	for n := 0; n < len(urls); n++ {
		select {
		case s := <-ch:
			slots[s.i] = s.page
		case <-ctx.Done():
			// Out of budget. Return what has LANDED rather than nothing: pages
			// already read are worth more to the synthesis than a clean slate.
			return landed(slots)
		}
	}
	return landed(slots)
}

// landed drops the slots nothing came back for, in rank order.
//
// The seam promises "the pages that came back", so handing out zero-value Pages
// would break that contract for every caller — a URL of "" matches no Source, and a
// cancelled crawl would report a list of nothing-shaped pages instead of an empty
// one.
func landed(slots []Page) []Page {
	out := make([]Page, 0, len(slots))
	for _, p := range slots {
		if p.URL != "" && p.Markdown != "" {
			out = append(out, p)
		}
	}
	return out
}
