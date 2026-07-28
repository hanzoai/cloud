package crawl

import (
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// extract turns a document into the readable part plus what the page says about
// itself. It is separated from Fetch so the hard part — deciding which subtree is
// the article — is testable from a string, with no network.
//
// The approach is the long-settled readability heuristic: score block elements by
// how much of their text is prose rather than links, and take the best one. It is
// not a parser of any particular site's markup and deliberately has no per-domain
// rules; those rot the moment a site redesigns, and there is no bound on how many
// of them a crawler would eventually carry.
func extract(r io.Reader, base *url.URL) (*Page, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, err
	}

	page := &Page{Metadata: map[string]any{}}
	readMeta(doc, page)

	// Drop the furniture before scoring. Scoring first and dropping after would let
	// a large <nav> win on raw text volume alone.
	prune(doc)

	best := bestBlock(doc)
	if best == nil {
		best = doc // A document with no block candidate still has its text.
	}
	stripLinkLists(best)
	page.Markdown = render(best, base)
	if page.Title == "" {
		page.Title = firstHeading(best)
	}
	return page, nil
}

// furniture is the set of elements that are never article content. Removing them
// outright is safe in a way that down-weighting is not: a long sidebar can outscore
// a short article on volume, and the failure is silent — a plausible-looking result
// made entirely of navigation.
var furniture = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true, atom.Iframe: true,
	atom.Nav: true, atom.Footer: true, atom.Aside: true, atom.Form: true,
	atom.Button: true, atom.Svg: true, atom.Canvas: true, atom.Template: true,
	atom.Object: true, atom.Embed: true,
}

// prune removes furniture and comments in one pass.
func prune(n *html.Node) {
	var next *html.Node
	for c := n.FirstChild; c != nil; c = next {
		next = c.NextSibling // captured first: c may be detached below
		switch {
		case c.Type == html.CommentNode:
			n.RemoveChild(c)
		case c.Type == html.ElementNode && furniture[c.DataAtom]:
			n.RemoveChild(c)
		case c.Type == html.ElementNode && hidden(c):
			n.RemoveChild(c)
		default:
			prune(c)
		}
	}
}

// hidden reports whether an element is explicitly not rendered. Content behind
// hidden/aria-hidden is not what a reader sees, so including it would put text in
// the result that no human found on the page.
func hidden(n *html.Node) bool {
	for _, a := range n.Attr {
		switch strings.ToLower(a.Key) {
		case "hidden":
			return true
		case "aria-hidden":
			if strings.EqualFold(a.Val, "true") {
				return true
			}
		case "style":
			v := strings.ToLower(strings.ReplaceAll(a.Val, " ", ""))
			if strings.Contains(v, "display:none") || strings.Contains(v, "visibility:hidden") {
				return true
			}
		}
	}
	return false
}

// stripLinkLists removes link lists from INSIDE the chosen block.
//
// Scoring picks the best container, but chrome that lives within it rides along:
// the winning block's overall density stays low because the article dominates it,
// so a link list nested inside is never judged on its own. Wikipedia is the case
// that showed this — the interlanguage sidebar (Afrikaans, العربية, …) landed at
// the top of the extracted article, where it reads as though the page began with a
// list of languages.
//
// So each nested container is judged on ITS OWN density, after a winner is chosen.
// Only containers are considered: a <p> full of citations is prose that happens to
// link, and removing it would delete the sentence around the links.
func stripLinkLists(root *html.Node) {
	var next *html.Node
	for c := root.FirstChild; c != nil; c = next {
		next = c.NextSibling
		if c.Type != html.ElementNode {
			continue
		}
		if container[c.DataAtom] && linkList(c) {
			root.RemoveChild(c)
			continue
		}
		stripLinkLists(c)
	}
}

// container elements can hold a link list. <p> is deliberately absent — see above.
var container = map[atom.Atom]bool{
	atom.Ul: true, atom.Ol: true, atom.Div: true, atom.Section: true,
	atom.Table: true, atom.Dl: true, atom.Menu: true,
}

// linkList reports whether n is mostly link text.
//
// The 0.5 threshold matches the block scorer, so a container is judged the same way
// whether it is competing to BE the article or sitting inside it. Very short
// containers are left alone: a two-word div that happens to be a link is a byline
// or a tag, and the density measure is meaningless at that size.
func linkList(n *html.Node) bool {
	total := textLen(n)
	if total < 40 {
		return false
	}
	return float64(linkTextLen(n))/float64(total) > 0.5
}

// candidate elements are the ones that plausibly wrap an article.
var candidate = map[atom.Atom]bool{
	atom.Article: true, atom.Main: true, atom.Section: true,
	atom.Div: true, atom.Body: true, atom.Td: true,
}

// bestBlock picks the subtree most likely to be the article.
func bestBlock(root *html.Node) *html.Node {
	var best *html.Node
	bestScore := 0.0
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && candidate[n.DataAtom] {
			if s := score(n); s > bestScore {
				bestScore, best = s, n
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	// <article> and <main> are explicit authorial intent. When the page provides one
	// and it holds real text, trust it over a div that merely scored higher by
	// wrapping more of the page.
	if e := firstOf(root, atom.Article, atom.Main); e != nil && textLen(e) > 200 {
		return e
	}
	return best
}

// score rewards prose and punishes links.
//
// Link density is the load-bearing half. Text volume alone cannot tell an article
// from a link farm — a footer sitemap or a "related posts" rail is mostly text by
// character count, and every character of it is inside an <a>. Prose is the text
// that is NOT linked, so that is what is counted, and a block that is more than
// about half links is rejected outright however large it is.
func score(n *html.Node) float64 {
	total := textLen(n)
	if total < 100 {
		return 0
	}
	linked := linkTextLen(n)
	density := float64(linked) / float64(total)
	if density > 0.5 {
		return 0
	}
	prose := float64(total-linked) * (1 - density)
	// Paragraph count separates one long run-on block from actual structured
	// article text; commas are the classic readability proxy for sentence-ness and
	// survive markup that uses <br> instead of <p>.
	prose += float64(count(n, atom.P)) * 25
	prose += float64(strings.Count(textOf(n), ",")) * 3
	return prose
}

func textLen(n *html.Node) int { return len(strings.TrimSpace(textOf(n))) }

// linkTextLen is the number of characters inside <a> descendants.
func linkTextLen(n *html.Node) int {
	total := 0
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.ElementNode && x.DataAtom == atom.A {
			total += len(strings.TrimSpace(textOf(x)))
			return // do not double count nested markup inside the link
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return total
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func count(n *html.Node, a atom.Atom) int {
	total := 0
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.ElementNode && x.DataAtom == a {
			total++
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return total
}

func firstOf(n *html.Node, want ...atom.Atom) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			for _, w := range want {
				if c.DataAtom == w {
					return c
				}
			}
		}
		if got := firstOf(c, want...); got != nil {
			return got
		}
	}
	return nil
}

func firstHeading(n *html.Node) string {
	if h := firstOf(n, atom.H1, atom.H2); h != nil {
		return squeeze(textOf(h))
	}
	return ""
}

// readMeta collects <title> and the <meta> tags, before pruning removes anything.
//
// og:* wins over the plain tags where both exist: a page that bothered to write
// Open Graph is describing itself to a reader elsewhere, which is exactly this
// caller's position, and its plain <title> is more likely to carry site-name
// boilerplate.
func readMeta(doc *html.Node, page *Page) {
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.DataAtom {
			case atom.Title:
				if page.Title == "" {
					page.Title = squeeze(textOf(n))
				}
			case atom.Html:
				if lang := attr(n, "lang"); lang != "" {
					page.Metadata["language"] = lang
				}
			case atom.Meta:
				name := strings.ToLower(attr(n, "name"))
				if name == "" {
					name = strings.ToLower(attr(n, "property"))
				}
				content := squeeze(attr(n, "content"))
				if name == "" || content == "" {
					break
				}
				switch name {
				case "description", "og:description":
					if _, ok := page.Metadata["description"]; !ok || name == "og:description" {
						page.Metadata["description"] = content
					}
				case "og:title":
					page.Title = content
				case "og:image", "og:site_name", "og:type", "author", "keywords":
					page.Metadata[strings.TrimPrefix(name, "og:")] = content
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if page.Title != "" {
		page.Metadata["title"] = page.Title
	}
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

// squeeze collapses all whitespace runs to single spaces and trims. HTML treats
// newlines and indentation as insignificant, so text carried straight out of a
// prettified document is full of runs that would otherwise become real whitespace
// in the markdown.
func squeeze(s string) string { return strings.Join(strings.Fields(s), " ") }
