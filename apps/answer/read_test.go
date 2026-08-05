package answer

// read_test.go — proofs for the READ stage: it enriches the snippet with the
// fetched page, it never re-identifies a source, it is bounded, and EVERY failure
// path degrades to the search snippet instead of breaking the answer.

import (
	"context"
	crawlpkg "github.com/hanzoai/cloud/apps/crawl"
	luxlog "github.com/luxfi/log"
	"strings"
	"testing"
	"time"
)

// fakeCrawl swaps the ONE crawl binding for the duration of a test and records
// the URLs it was asked for. No test ever dials the crawl service.
func fakeCrawl(t *testing.T, pages map[string]string) *[]string {
	t.Helper()
	var asked []string
	prev := crawl
	crawl = func(_ context.Context, _ luxlog.Logger, _ crawlpkg.Scope, urls []string) []Page {
		asked = append(asked, urls...)
		out := make([]Page, 0, len(urls))
		for _, u := range urls {
			if md, ok := pages[u]; ok {
				out = append(out, Page{URL: u, Markdown: md})
			}
		}
		return out
	}
	t.Cleanup(func() { crawl = prev })
	return &asked
}

func srcs(urls ...string) []Source {
	out := make([]Source, 0, len(urls))
	for _, u := range urls {
		out = append(out, Source{URL: u, Title: "T " + u, Snippet: "snippet " + u, Engine: "bing", Favicon: "f " + u})
	}
	return out
}

func TestReadFillsTextKeepingIdentity(t *testing.T) {
	asked := fakeCrawl(t, map[string]string{"https://a.com/x": "# A\n\nThe full page body."})
	in := srcs("https://a.com/x", "https://b.com/y")
	var progress []string
	out := read(context.Background(), nil, crawlpkg.Scope{}, in,
		urlsOf(in), maxPageText, func(h string) { progress = append(progress, h) })

	if !strings.Contains(out[0].Text, "The full page body.") {
		t.Fatalf("fetched page must land in Text, got %q", out[0].Text)
	}
	// THE WIRE IS UNTOUCHED. Snippet is what the `sources` frame carries, and the
	// fetched page — thousands of runes we did not author — must never displace it.
	if out[0].Snippet != "snippet https://a.com/x" {
		t.Fatalf("read must not put page text on the wire, got %q", out[0].Snippet)
	}
	// identity is untouched — the `sources` frame the client already rendered stays valid.
	if out[0].URL != "https://a.com/x" || out[0].Title != "T https://a.com/x" ||
		out[0].Engine != "bing" || out[0].Favicon != "f https://a.com/x" {
		t.Fatalf("read must not re-identify a source: %+v", out[0])
	}
	// a source the crawl did not return has no page text at all.
	if out[1].Text != "" {
		t.Fatalf("un-fetched source must have no page text, got %q", out[1].Text)
	}
	if len(*asked) != 2 {
		t.Fatalf("both sources should have been requested, got %v", *asked)
	}
	// Reading progress is emitted PER SOURCE, by host — the extension shows which
	// page is being read, not a single opaque "reading" for the whole batch.
	if strings.Join(progress, ",") != "a.com,b.com" {
		t.Fatalf("onRead must fire once per url, by host, got %v", progress)
	}
}

// urlsOf is the read stage's caller-side url list, spelled out in tests the same
// way survey() builds it.
func urlsOf(s []Source) []string {
	out := make([]string, 0, len(s))
	for _, x := range s {
		out = append(out, x.URL)
	}
	return out
}

func TestReadTopZeroIsNoOp(t *testing.T) {
	asked := fakeCrawl(t, map[string]string{"https://a.com/x": "page"})
	out := read(context.Background(), nil, crawlpkg.Scope{}, srcs("https://a.com/x"), nil, maxPageText, nil)
	if len(*asked) != 0 {
		t.Fatalf("no urls must not crawl, asked %v", *asked)
	}
	if out[0].Text != "" || out[0].Snippet != "snippet https://a.com/x" {
		t.Fatalf("no urls must leave the source untouched, got %+v", out[0])
	}
}

// TestReadBoundedByCeilingAndSources proves the cost bound: a mode can never ask
// for more pages than the hard ceiling, and never for more sources than it has.
func TestReadBoundedByCeilingAndSources(t *testing.T) {
	asked := fakeCrawl(t, nil)
	many := make([]string, 0, 20)
	for _, h := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"} {
		many = append(many, "https://"+h+".com/x")
	}
	read(context.Background(), nil, crawlpkg.Scope{}, srcs(many...), many, maxPageText, nil)
	if len(*asked) != maxRead {
		t.Fatalf("read must cap at %d pages, asked %d", maxRead, len(*asked))
	}

	asked2 := fakeCrawl(t, nil)
	one := srcs("https://only.com/x")
	read(context.Background(), nil, crawlpkg.Scope{}, one, urlsOf(one), maxPageText, nil)
	if len(*asked2) != 1 {
		t.Fatalf("read must not ask for more sources than exist, asked %v", *asked2)
	}
}

// TestReadDegradesOnCrawlFailure proves the whole point of best-effort: a crawl
// that returns nothing (service down, timeout, blocked page) leaves the answer
// grounded on the search snippets rather than empty.
func TestReadDegradesOnCrawlFailure(t *testing.T) {
	fakeCrawl(t, nil) // returns no pages for anything
	out := read(context.Background(), nil, crawlpkg.Scope{}, srcs("https://a.com/x"),
		[]string{"https://a.com/x"}, maxPageText, nil)
	if out[0].Text != "" || out[0].Snippet != "snippet https://a.com/x" {
		t.Fatalf("a failed crawl must leave the source on its snippet, got %+v", out[0])
	}
}

// TestReadEmptyPageKeepsSnippet proves a page that fetched "successfully" but
// carries no text is treated as a failure, not as an empty grounding.
func TestReadEmptyPageKeepsSnippet(t *testing.T) {
	fakeCrawl(t, map[string]string{"https://a.com/x": "   \n\t "})
	out := read(context.Background(), nil, crawlpkg.Scope{}, srcs("https://a.com/x"),
		[]string{"https://a.com/x"}, maxPageText, nil)
	if out[0].Text != "" || out[0].Snippet != "snippet https://a.com/x" {
		t.Fatalf("blank page must leave the source on its snippet, got %+v", out[0])
	}
}

// TestReadClipsPageText proves the synthesis prompt cannot be blown up by a huge
// page: the fetched text is clipped to the read budget.
func TestReadClipsPageText(t *testing.T) {
	fakeCrawl(t, map[string]string{"https://a.com/x": strings.Repeat("x", maxPageText*3)})
	out := read(context.Background(), nil, crawlpkg.Scope{}, srcs("https://a.com/x"),
		[]string{"https://a.com/x"}, maxPageText, nil)
	if n := len([]rune(out[0].Text)); n != maxPageText {
		t.Fatalf("page text must clip to %d runes, got %d", maxPageText, n)
	}
}

// TestCrawlPagesHonorsContext proves the real binding cannot wedge the loop: a
// cancelled ctx returns immediately even though the underlying crawl blocks.
func TestCrawlPagesHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan []Page, 1)
	go func() { done <- crawlPages(ctx, nil, crawlpkg.Scope{}, []string{"https://never.example/x"}) }()
	select {
	case pages := <-done:
		if len(pages) != 0 {
			t.Fatalf("a cancelled crawl must yield no pages, got %v", pages)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("crawlPages ignored a cancelled context")
	}
}
