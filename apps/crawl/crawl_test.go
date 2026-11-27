package crawl

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/hanzoai/outbound"
)

// ── the address guard ───────────────────────────────────────────────────────
//
// The address rule itself is hanzoai/outbound's, tested there range by range.
// What is crawl's is that every fetch goes through it.

// Fetch must refuse a blocked destination before any connection is attempted, and
// say so distinguishably: an operator reading a log needs to tell an attack from an
// outage.
func TestFetchRefusesInternalAddress(t *testing.T) {
	for _, raw := range []string{
		"http://127.0.0.1/",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]/",
		"http://10.0.0.1/",
	} {
		_, err := Fetch(context.Background(), raw)
		if err == nil {
			t.Fatalf("Fetch(%q) succeeded — an internal address must never be dialled", raw)
		}
		if !errors.Is(err, outbound.ErrRefused) {
			t.Errorf("Fetch(%q) error = %v, want the blocked-address error", raw, err)
		}
	}
}

func TestFetchRefusesNonHTTPSchemes(t *testing.T) {
	for _, raw := range []string{
		"file:///etc/passwd",
		"gopher://example.com/",
		"ftp://example.com/",
	} {
		if _, err := Fetch(context.Background(), raw); err == nil {
			t.Fatalf("Fetch(%q) succeeded — only http(s) is fetchable", raw)
		}
	}
}

// ── extraction ──────────────────────────────────────────────────────────────

func parse(t *testing.T, html, base string) *Page {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("bad base: %v", err)
	}
	p, err := extract(strings.NewReader(html), u)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	return p
}

// The whole point of the extractor: return the article, not the page. A real page
// is mostly navigation, and a crawler that returns the nav is worse than useless to
// a model — it is confidently wrong.
func TestExtractPicksArticleOverNavigation(t *testing.T) {
	page := parse(t, `<html><head><title>T</title></head><body>
	  <nav><a href="/a">Home</a><a href="/b">Products</a><a href="/c">Pricing</a></nav>
	  <aside><a href="/x">Related one</a><a href="/y">Related two</a></aside>
	  <article>
	    <h1>The Heading</h1>
	    <p>`+strings.Repeat("Real prose that a reader came here for, with commas, and length. ", 6)+`</p>
	  </article>
	  <footer><a href="/p">Privacy</a><a href="/t">Terms</a></footer>
	</body></html>`, "https://example.com/post")

	if !strings.Contains(page.Markdown, "Real prose") {
		t.Fatalf("article text missing:\n%s", page.Markdown)
	}
	for _, chrome := range []string{"Home", "Pricing", "Privacy", "Terms", "Related one"} {
		if strings.Contains(page.Markdown, chrome) {
			t.Errorf("navigation leaked into the content: %q\n%s", chrome, page.Markdown)
		}
	}
}

// REGRESSION: a link list nested INSIDE the winning block used to survive, because
// scoring judges containers only while they compete to be the article. Found on
// Wikipedia, whose interlanguage sidebar (Afrikaans, العربية, …) came back at the
// top of every article — the extracted page read as though it began with a list of
// languages. Each nested container is now judged on its own density.
func TestExtractDropsLinkListInsideTheArticle(t *testing.T) {
	page := parse(t, `<html><body><article>
	  <div id="languages">
	    <ul>
	      <li><a href="/af">Afrikaans</a></li><li><a href="/ar">العربية</a></li>
	      <li><a href="/az">Azərbaycanca</a></li><li><a href="/ca">Català</a></li>
	      <li><a href="/cs">Čeština</a></li><li><a href="/de">Deutsch</a></li>
	    </ul>
	  </div>
	  <h1>Web crawler</h1>
	  <p>`+strings.Repeat("A crawler is a bot that browses the web, methodically, with commas. ", 8)+`</p>
	</article></body></html>`, "https://example.com/wiki/Web_crawler")

	md := page.Markdown
	for _, lang := range []string{"Afrikaans", "Azərbaycanca", "Čeština", "Deutsch"} {
		if strings.Contains(md, lang) {
			t.Errorf("interlanguage list leaked into the article: %q\n%s", lang, md)
		}
	}
	if !strings.Contains(md, "# Web crawler") || !strings.Contains(md, "A crawler is a bot") {
		t.Errorf("the actual article was lost with the sidebar:\n%s", md)
	}
}

// The counterpart: prose that merely CONTAINS links is not a link list. A <p> of
// citations must survive, or removing chrome starts deleting sentences.
func TestExtractKeepsProseWithLinks(t *testing.T) {
	page := parse(t, `<html><body><article>
	  <p>The <a href="/a">first study</a> and the <a href="/b">second study</a> both found
	     that crawlers, when polite, reduce load, which matters for operators, and
	     `+strings.Repeat("this sentence continues with commas, at length. ", 6)+`</p>
	</article></body></html>`, "https://example.com/")

	if !strings.Contains(page.Markdown, "both found") {
		t.Errorf("prose containing links was removed as if it were a link list:\n%s", page.Markdown)
	}
}

// Script and style hold text that is not prose. Left in, it lands in the output as
// confident nonsense.
func TestExtractDropsScriptAndStyle(t *testing.T) {
	page := parse(t, `<html><body><article>
	  <script>var secret = "do not print me";</script>
	  <style>.a{color:red}</style>
	  <p>`+strings.Repeat("Body text with commas, and more. ", 8)+`</p>
	</article></body></html>`, "https://example.com/")

	for _, leak := range []string{"do not print me", "color:red"} {
		if strings.Contains(page.Markdown, leak) {
			t.Errorf("%q leaked into content:\n%s", leak, page.Markdown)
		}
	}
}

func TestExtractReadsMetadata(t *testing.T) {
	page := parse(t, `<html lang="en"><head>
	  <title>Plain Title</title>
	  <meta name="description" content="A plain description.">
	  <meta property="og:title" content="OG Title">
	  <meta property="og:description" content="An OG description.">
	</head><body><article><p>`+strings.Repeat("Text, text. ", 20)+`</p></article></body></html>`,
		"https://example.com/")

	// og:* wins where both exist — see readMeta.
	if page.Title != "OG Title" {
		t.Errorf("title = %q, want the og:title", page.Title)
	}
	if page.Metadata["description"] != "An OG description." {
		t.Errorf("description = %v, want the og:description", page.Metadata["description"])
	}
	if page.Metadata["language"] != "en" {
		t.Errorf("language = %v, want en", page.Metadata["language"])
	}
}

// Content hidden from a reader must be hidden from the crawler: text nobody can see
// on the page should not appear in a summary of it.
func TestExtractSkipsHiddenContent(t *testing.T) {
	page := parse(t, `<html><body><article>
	  <p>`+strings.Repeat("Visible prose, with commas. ", 8)+`</p>
	  <div style="display:none">invisible marker</div>
	  <div aria-hidden="true">aria marker</div>
	  <div hidden>hidden marker</div>
	</article></body></html>`, "https://example.com/")

	for _, leak := range []string{"invisible marker", "aria marker", "hidden marker"} {
		if strings.Contains(page.Markdown, leak) {
			t.Errorf("%q leaked:\n%s", leak, page.Markdown)
		}
	}
}

// ── rendering ───────────────────────────────────────────────────────────────

func TestRenderStructure(t *testing.T) {
	page := parse(t, `<html><body><article>
	  <h1>Title</h1>
	  <h2>Sub</h2>
	  <p>Some <strong>bold</strong> and <em>italic</em> and <code>code</code>, with a comma.</p>
	  <ul><li>one</li><li>two</li></ul>
	  <ol><li>first</li><li>second</li></ol>
	  <blockquote><p>Quoted line, with a comma.</p></blockquote>
	  <pre>fn main() {}</pre>
	  <p>`+strings.Repeat("Filler to win the block score, with commas. ", 6)+`</p>
	</article></body></html>`, "https://example.com/")

	md := page.Markdown
	for _, want := range []string{
		"# Title", "## Sub",
		"**bold**", "*italic*", "`code`",
		"- one", "- two",
		"1. first", "2. second",
		"> Quoted line",
		"```",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q:\n%s", want, md)
		}
	}
}

// A relative href is a dead link by the time anyone reads the output, so links are
// resolved against the page they were found on. javascript:/data: are dropped —
// they are not links a reader can follow.
func TestRenderResolvesAndFiltersLinks(t *testing.T) {
	page := parse(t, `<html><body><article>
	  <p><a href="/rel/path">relative</a>, <a href="https://other.test/abs">absolute</a>,
	     <a href="javascript:alert(1)">script</a>, <a href="#frag">fragment</a>, with commas.</p>
	  <p>`+strings.Repeat("Filler prose, with commas. ", 8)+`</p>
	</article></body></html>`, "https://example.com/dir/page.html")

	md := page.Markdown
	if !strings.Contains(md, "[relative](https://example.com/rel/path)") {
		t.Errorf("relative link not resolved against the base:\n%s", md)
	}
	if !strings.Contains(md, "[absolute](https://other.test/abs)") {
		t.Errorf("absolute link not preserved:\n%s", md)
	}
	if strings.Contains(md, "javascript:") {
		t.Errorf("javascript: URL survived into the output:\n%s", md)
	}
	// The text is kept even though the destination is dropped — the sentence still
	// reads correctly without it.
	if !strings.Contains(md, "script") {
		t.Errorf("link text was dropped along with its unusable href:\n%s", md)
	}
}

func TestRenderTable(t *testing.T) {
	page := parse(t, `<html><body><article>
	  <table>
	    <tr><th>Name</th><th>Value</th></tr>
	    <tr><td>alpha</td><td>1</td></tr>
	    <tr><td>beta</td><td>2</td></tr>
	  </table>
	  <p>`+strings.Repeat("Filler prose, with commas. ", 8)+`</p>
	</article></body></html>`, "https://example.com/")

	md := page.Markdown
	for _, want := range []string{"| Name | Value |", "| --- | --- |", "| alpha | 1 |", "| beta | 2 |"} {
		if !strings.Contains(md, want) {
			t.Errorf("table missing %q:\n%s", want, md)
		}
	}
}

// A ragged table is a layout table. Forcing it into a grid produces a wide, empty
// block; rendering the cells as prose keeps the text readable.
func TestRenderRaggedTableFallsBackToProse(t *testing.T) {
	page := parse(t, `<html><body><article>
	  <table><tr><td>`+strings.Repeat("Layout cell prose, with commas. ", 8)+`</td></tr>
	         <tr><td>a</td><td>b</td></tr></table>
	</article></body></html>`, "https://example.com/")

	if strings.Contains(page.Markdown, "| --- |") {
		t.Errorf("ragged table was forced into a grid:\n%s", page.Markdown)
	}
	if !strings.Contains(page.Markdown, "Layout cell prose") {
		t.Errorf("layout table text was lost:\n%s", page.Markdown)
	}
}

// Nested lists must stay nested: flattening loses the relationship the markup
// encoded, and hoisting the sublist's text into its parent item makes a wrong one.
func TestRenderNestedList(t *testing.T) {
	page := parse(t, `<html><body><article>
	  <ul><li>outer<ul><li>inner</li></ul></li></ul>
	  <p>`+strings.Repeat("Filler prose, with commas. ", 8)+`</p>
	</article></body></html>`, "https://example.com/")

	md := page.Markdown
	if !strings.Contains(md, "- outer") {
		t.Errorf("outer item missing:\n%s", md)
	}
	if !strings.Contains(md, "  - inner") {
		t.Errorf("inner item not indented under its parent:\n%s", md)
	}
	if strings.Contains(md, "- outerinner") {
		t.Errorf("sublist text was hoisted into the parent item:\n%s", md)
	}
}

// A page that is genuinely all chrome yields little content. That is a true answer,
// not an error — a caller that treated it as failure would retry forever.
func TestExtractEmptyPageIsNotAnError(t *testing.T) {
	page := parse(t, `<html><body><nav><a href="/">Home</a></nav></body></html>`, "https://example.com/")
	if page == nil {
		t.Fatal("extract returned nil for a content-free page")
	}
}

func TestDocumentContentTypes(t *testing.T) {
	for _, ct := range []string{"text/html", "text/html; charset=utf-8", "TEXT/HTML", "application/xhtml+xml", "text/plain"} {
		if !document(ct) {
			t.Errorf("document(%q) = false, want true", ct)
		}
	}
	for _, ct := range []string{"image/png", "application/pdf", "application/octet-stream", "video/mp4"} {
		if document(ct) {
			t.Errorf("document(%q) = true — a non-document must not reach the HTML parser", ct)
		}
	}
}
