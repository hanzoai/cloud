package answer

// read_test.go — proofs for the READ stage: it enriches the snippet with the
// fetched page, it never re-identifies a source, it is bounded, and EVERY failure
// path degrades to the search snippet instead of breaking the answer.

import (
	"context"
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
	crawl = func(_ context.Context, urls []string) []Page {
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

func TestReadEnrichesSnippetKeepingIdentity(t *testing.T) {
	asked := fakeCrawl(t, map[string]string{"https://a.com/x": "# A\n\nThe full page body."})
	in := srcs("https://a.com/x", "https://b.com/y")
	out := read(context.Background(), in, 2)

	if !strings.Contains(out[0].Snippet, "The full page body.") {
		t.Fatalf("fetched page must replace the snippet, got %q", out[0].Snippet)
	}
	// identity is untouched — the `sources` frame the client already rendered stays valid.
	if out[0].URL != "https://a.com/x" || out[0].Title != "T https://a.com/x" ||
		out[0].Engine != "bing" || out[0].Favicon != "f https://a.com/x" {
		t.Fatalf("read must not re-identify a source: %+v", out[0])
	}
	// a source the crawl did not return keeps its search snippet.
	if out[1].Snippet != "snippet https://b.com/y" {
		t.Fatalf("un-fetched source must keep its snippet, got %q", out[1].Snippet)
	}
	if len(*asked) != 2 {
		t.Fatalf("both sources should have been requested, got %v", *asked)
	}
}

func TestReadTopZeroIsNoOp(t *testing.T) {
	asked := fakeCrawl(t, map[string]string{"https://a.com/x": "page"})
	out := read(context.Background(), srcs("https://a.com/x"), 0)
	if len(*asked) != 0 {
		t.Fatalf("top=0 must not crawl, asked %v", *asked)
	}
	if out[0].Snippet != "snippet https://a.com/x" {
		t.Fatalf("top=0 must leave snippets untouched, got %q", out[0].Snippet)
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
	read(context.Background(), srcs(many...), 99)
	if len(*asked) != maxRead {
		t.Fatalf("read must cap at %d pages, asked %d", maxRead, len(*asked))
	}

	asked2 := fakeCrawl(t, nil)
	read(context.Background(), srcs("https://only.com/x"), 6)
	if len(*asked2) != 1 {
		t.Fatalf("read must not ask for more sources than exist, asked %v", *asked2)
	}
}

// TestReadDegradesOnCrawlFailure proves the whole point of best-effort: a crawl
// that returns nothing (service down, timeout, blocked page) leaves the answer
// grounded on the search snippets rather than empty.
func TestReadDegradesOnCrawlFailure(t *testing.T) {
	fakeCrawl(t, nil) // returns no pages for anything
	out := read(context.Background(), srcs("https://a.com/x"), 4)
	if out[0].Snippet != "snippet https://a.com/x" {
		t.Fatalf("a failed crawl must preserve the snippet, got %q", out[0].Snippet)
	}
}

// TestReadEmptyPageKeepsSnippet proves a page that fetched "successfully" but
// carries no text is treated as a failure, not as an empty grounding.
func TestReadEmptyPageKeepsSnippet(t *testing.T) {
	fakeCrawl(t, map[string]string{"https://a.com/x": "   \n\t "})
	out := read(context.Background(), srcs("https://a.com/x"), 4)
	if out[0].Snippet != "snippet https://a.com/x" {
		t.Fatalf("blank page must preserve the snippet, got %q", out[0].Snippet)
	}
}

// TestReadClipsPageText proves the synthesis prompt cannot be blown up by a huge
// page: the fetched text is clipped to the read budget.
func TestReadClipsPageText(t *testing.T) {
	fakeCrawl(t, map[string]string{"https://a.com/x": strings.Repeat("x", maxPageText*3)})
	out := read(context.Background(), srcs("https://a.com/x"), 4)
	if n := len([]rune(out[0].Snippet)); n != maxPageText {
		t.Fatalf("page text must clip to %d runes, got %d", maxPageText, n)
	}
}

// TestCrawlPagesHonorsContext proves the real binding cannot wedge the loop: a
// cancelled ctx returns immediately even though the underlying crawl blocks.
func TestCrawlPagesHonorsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan []Page, 1)
	go func() { done <- crawlPages(ctx, []string{"https://never.example/x"}) }()
	select {
	case pages := <-done:
		if len(pages) != 0 {
			t.Fatalf("a cancelled crawl must yield no pages, got %v", pages)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("crawlPages ignored a cancelled context")
	}
}
