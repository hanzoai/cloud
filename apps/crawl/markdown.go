package crawl

import (
	"net/url"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// render walks the chosen subtree and writes markdown.
//
// Markdown rather than plain text because the consumer is a language model, and
// structure is signal: a heading tells it where a section starts, a list tells it
// the items are peers, a fenced block tells it not to read code as prose. Flattening
// all of that to a wall of text throws away information the page already encoded.
//
// Only constructs that survive the round trip are emitted. There is no attempt at
// full HTML→markdown fidelity: markup with no markdown equivalent renders as its
// text, which reads correctly, rather than as an approximation that reads like a
// mistake.
func render(n *html.Node, base *url.URL) string {
	var w writer
	w.base = base
	w.block(n)
	return strings.TrimSpace(w.out.String())
}

type writer struct {
	out  strings.Builder
	base *url.URL
	// depth is list nesting, used for indentation.
	depth int
	// order is the item counter per list level; nil means the level is unordered.
	order []int
}

// block renders n as block-level content, emitting paragraph breaks between
// children. Inline content encountered here is gathered into a paragraph.
func (w *writer) block(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch {
		case c.Type == html.TextNode:
			if s := squeeze(c.Data); s != "" {
				w.para(s)
			}
		case c.Type != html.ElementNode:
			// nothing
		default:
			w.element(c)
		}
	}
}

func (w *writer) element(n *html.Node) {
	switch n.DataAtom {
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		if s := squeeze(w.inline(n)); s != "" {
			w.para(strings.Repeat("#", level(n.DataAtom)) + " " + s)
		}

	case atom.P:
		if s := squeeze(w.inline(n)); s != "" {
			w.para(s)
		}

	case atom.Br:
		w.out.WriteString("\n")

	case atom.Hr:
		w.para("---")

	case atom.Ul, atom.Ol:
		w.list(n)

	case atom.Blockquote:
		// Rendered by prefixing every line, including the ones the nested content
		// produced — a quote containing a list must stay inside the quote.
		var inner writer
		inner.base = w.base
		inner.block(n)
		for _, line := range strings.Split(strings.TrimSpace(inner.out.String()), "\n") {
			w.para("> " + line)
		}

	case atom.Pre:
		if s := strings.TrimRight(textOf(n), "\n"); strings.TrimSpace(s) != "" {
			// Fenced, not indented: an indented block is ambiguous inside a list item
			// and silently becomes part of the item's prose.
			w.para("```\n" + s + "\n```")
		}

	case atom.Table:
		w.table(n)

	case atom.Img:
		if s := w.image(n); s != "" {
			w.para(s)
		}

	case atom.A, atom.Span, atom.Strong, atom.B, atom.Em, atom.I, atom.Code, atom.Del, atom.S:
		// Inline element sitting directly in a block context — real pages do this
		// constantly. Treat it as its own paragraph rather than dropping it.
		if s := squeeze(w.inline(n)); s != "" {
			w.para(s)
		}

	default:
		w.block(n)
	}
}

// inline renders n's subtree as a single line of markdown.
func (w *writer) inline(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch {
		case c.Type == html.TextNode:
			b.WriteString(c.Data)

		case c.Type != html.ElementNode:

		default:
			switch c.DataAtom {
			case atom.A:
				text := squeeze(w.inline(c))
				href := w.resolve(attr(c, "href"))
				switch {
				case text == "":
				case href == "":
					b.WriteString(text)
				default:
					b.WriteString("[" + text + "](" + href + ")")
				}
			case atom.Strong, atom.B:
				if s := squeeze(w.inline(c)); s != "" {
					b.WriteString("**" + s + "**")
				}
			case atom.Em, atom.I:
				if s := squeeze(w.inline(c)); s != "" {
					b.WriteString("*" + s + "*")
				}
			case atom.Code:
				if s := squeeze(w.inline(c)); s != "" {
					b.WriteString("`" + s + "`")
				}
			case atom.Del, atom.S:
				if s := squeeze(w.inline(c)); s != "" {
					b.WriteString("~~" + s + "~~")
				}
			case atom.Br:
				b.WriteString(" ")
			case atom.Img:
				b.WriteString(w.image(c))
			default:
				b.WriteString(w.inline(c))
			}
		}
	}
	return b.String()
}

func (w *writer) list(n *html.Node) {
	ordered := n.DataAtom == atom.Ol
	w.depth++
	if ordered {
		w.order = append(w.order, 0)
	} else {
		w.order = append(w.order, -1)
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.DataAtom != atom.Li {
			continue
		}
		marker := "- "
		if ordered {
			w.order[len(w.order)-1]++
			marker = itoa(w.order[len(w.order)-1]) + ". "
		}
		indent := strings.Repeat("  ", w.depth-1)

		// An <li> holds inline text and may also hold nested lists. Render the
		// item's own text first, then let the nested list recurse at the deeper
		// indent — otherwise a sublist's text is hoisted into its parent item.
		var own strings.Builder
		var nested []*html.Node
		for x := c.FirstChild; x != nil; x = x.NextSibling {
			if x.Type == html.ElementNode && (x.DataAtom == atom.Ul || x.DataAtom == atom.Ol) {
				nested = append(nested, x)
				continue
			}
			switch {
			case x.Type == html.TextNode:
				own.WriteString(x.Data)
			case x.Type == html.ElementNode:
				own.WriteString(w.inline(x))
			}
		}
		if s := squeeze(own.String()); s != "" {
			w.line(indent + marker + s)
		}
		for _, sub := range nested {
			w.list(sub)
		}
	}
	w.order = w.order[:len(w.order)-1]
	w.depth--
	if w.depth == 0 {
		w.out.WriteString("\n")
	}
}

// table renders a GitHub-flavoured table when the shape is regular, and falls back
// to rendering the cells as prose when it is not. Layout tables are still common,
// and forcing one into a grid produces a wide, empty, unreadable block.
func (w *writer) table(n *html.Node) {
	var rows [][]string
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.ElementNode && x.DataAtom == atom.Tr {
			var cells []string
			for c := x.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode && (c.DataAtom == atom.Td || c.DataAtom == atom.Th) {
					cells = append(cells, strings.ReplaceAll(squeeze(w.inline(c)), "|", "\\|"))
				}
			}
			if len(cells) > 0 {
				rows = append(rows, cells)
			}
			return
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)

	if len(rows) < 2 {
		w.block(n) // not really tabular
		return
	}
	cols := len(rows[0])
	for _, r := range rows {
		if len(r) != cols {
			w.block(n)
			return
		}
	}
	w.line("| " + strings.Join(rows[0], " | ") + " |")
	w.line("|" + strings.Repeat(" --- |", cols))
	for _, r := range rows[1:] {
		w.line("| " + strings.Join(r, " | ") + " |")
	}
	w.out.WriteString("\n")
}

func (w *writer) image(n *html.Node) string {
	src := w.resolve(attr(n, "src"))
	if src == "" {
		return ""
	}
	return "![" + squeeze(attr(n, "alt")) + "](" + src + ")"
}

// resolve makes a URL absolute against the page it was found on. Links are the
// main reason a caller crawls at all — a relative href that is passed through
// unresolved is a dead link by the time anyone reads it.
func (w *writer) resolve(href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if w.base != nil {
		u = w.base.ResolveReference(u)
	}
	// Only http(s) survives. javascript: and data: are not links a reader can
	// follow, and passing them through would put executable text in the output.
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	return u.String()
}

// para writes s as its own block, separated by a blank line.
func (w *writer) para(s string) {
	if w.out.Len() > 0 {
		w.out.WriteString("\n\n")
	}
	w.out.WriteString(s)
}

// line writes s on its own line without a blank separator — for list items and
// table rows, which are one block made of many lines.
func (w *writer) line(s string) {
	if w.out.Len() > 0 && !strings.HasSuffix(w.out.String(), "\n") {
		w.out.WriteString("\n")
	}
	w.out.WriteString(s + "\n")
}

func level(a atom.Atom) int {
	switch a {
	case atom.H1:
		return 1
	case atom.H2:
		return 2
	case atom.H3:
		return 3
	case atom.H4:
		return 4
	case atom.H5:
		return 5
	default:
		return 6
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
