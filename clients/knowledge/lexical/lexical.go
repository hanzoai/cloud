// Package lexical builds a Lexical EditorState JSON string from block-structured
// content. It is the ONE place a KB importer turns markdown / HTML / plain text
// into the kb-page `body` shape the console's Lexical editor renders and the KB
// indexer flattens (clients/knowledge.lexicalText is the inverse reader).
//
// It is pure (no cloud, no I/O). Inline content — including "[[wikilinks]]" — is
// carried VERBATIM as text so link extraction sees exactly what the author wrote;
// this package deliberately does NOT parse inline marks (bold/italic/inline links)
// into Lexical formatting, keeping the conversion honest at block granularity.
package lexical

import (
	"encoding/json"
	"strings"

	"golang.org/x/net/html"
)

// Block is one block-level unit of a document. Text holds the block's inline text
// (newlines become Lexical linebreak nodes); Items holds a list's rows. The zero
// value is an empty paragraph.
type Block struct {
	Type  string   // "paragraph" | "heading" | "quote" | "code"
	Tag   string   // heading level: "h1".."h6" (heading only)
	Text  string   // inline text (paragraph/heading/quote/code)
	List  string   // "bullet" | "number" — set to render Items as a list block
	Items []string // list rows (when List is set)
}

// Build renders blocks to a Lexical EditorState JSON string. An empty block set
// yields a valid document with a single empty paragraph, so the editor always
// loads a well-formed state.
func Build(blocks []Block) string {
	children := make([]any, 0, len(blocks))
	for _, b := range blocks {
		if b.List != "" {
			children = append(children, listNode(b))
			continue
		}
		children = append(children, blockNode(b))
	}
	if len(children) == 0 {
		children = append(children, blockNode(Block{Type: "paragraph"}))
	}
	root := map[string]any{
		"root": map[string]any{
			"type": "root", "format": "", "indent": 0, "version": 1,
			"direction": "ltr", "children": children,
		},
	}
	out, _ := json.Marshal(root)
	return string(out)
}

// blockNode renders a non-list block (paragraph/heading/quote/code). An unknown
// type degrades to a paragraph so no content is dropped.
func blockNode(b Block) map[string]any {
	n := map[string]any{
		"format": "", "indent": 0, "version": 1, "direction": "ltr",
		"children": textNodes(b.Text),
	}
	switch b.Type {
	case "heading":
		n["type"] = "heading"
		n["tag"] = headingTag(b.Tag)
	case "quote":
		n["type"] = "quote"
	case "code":
		n["type"] = "code"
		n["language"] = ""
	default:
		n["type"] = "paragraph"
	}
	return n
}

// listNode renders a bullet/number list block with one listitem per row.
func listNode(b Block) map[string]any {
	items := make([]any, 0, len(b.Items))
	for i, it := range b.Items {
		items = append(items, map[string]any{
			"type": "listitem", "value": i + 1, "format": "", "indent": 0,
			"version": 1, "direction": "ltr", "children": textNodes(it),
		})
	}
	tag := "ul"
	if b.List == "number" {
		tag = "ol"
	}
	return map[string]any{
		"type": "list", "listType": b.List, "start": 1, "tag": tag,
		"format": "", "indent": 0, "version": 1, "direction": "ltr",
		"children": items,
	}
}

// textNodes splits inline text on newlines into Lexical text nodes separated by
// linebreak nodes, so multi-line block content keeps its line structure.
func textNodes(text string) []any {
	lines := strings.Split(text, "\n")
	out := make([]any, 0, len(lines)*2)
	for i, ln := range lines {
		if i > 0 {
			out = append(out, map[string]any{"type": "linebreak", "version": 1})
		}
		out = append(out, textNode(ln))
	}
	return out
}

func textNode(text string) map[string]any {
	return map[string]any{
		"type": "text", "detail": 0, "format": 0, "mode": "normal",
		"style": "", "text": text, "version": 1,
	}
}

func headingTag(tag string) string {
	switch tag {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		return tag
	default:
		return "h2"
	}
}

// FromPlain renders plain text: blank-line-separated paragraphs, each preserving
// its interior line breaks. It is the identity-preserving path for content with no
// markup (Evernote plain notes, a raw text import).
func FromPlain(text string) string {
	return Build(plainBlocks(text))
}

func plainBlocks(text string) []Block {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	var blocks []Block
	for _, para := range strings.Split(text, "\n\n") {
		p := strings.Trim(para, "\n")
		if strings.TrimSpace(p) == "" {
			continue
		}
		blocks = append(blocks, Block{Type: "paragraph", Text: p})
	}
	return blocks
}

// FromMarkdown parses markdown at BLOCK granularity — headings, fenced code,
// blockquotes, bullet/number lists, and paragraphs — keeping inline content
// verbatim (so "[[wikilinks]]" and other inline syntax survive untouched). It does
// not parse inline emphasis or inline links into Lexical marks.
func FromMarkdown(md string) string { return Build(MarkdownBlocks(md)) }

// MarkdownBlocks is the block parser behind FromMarkdown, exported so a normalizer
// that first rewrites links (Notion) can reuse the SAME block model.
func MarkdownBlocks(md string) []Block {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	lines := strings.Split(md, "\n")
	var blocks []Block
	var para []string
	flushPara := func() {
		if len(para) > 0 {
			blocks = append(blocks, Block{Type: "paragraph", Text: strings.Join(para, "\n")})
			para = nil
		}
	}
	for i := 0; i < len(lines); i++ {
		ln := lines[i]
		trimmed := strings.TrimSpace(ln)

		// Fenced code block: ``` ... ```
		if strings.HasPrefix(trimmed, "```") {
			flushPara()
			var code []string
			i++
			for i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
				code = append(code, lines[i])
				i++
			}
			blocks = append(blocks, Block{Type: "code", Text: strings.Join(code, "\n")})
			continue
		}

		if trimmed == "" {
			flushPara()
			continue
		}

		// ATX heading: #..###### followed by a space.
		if h := atxHeading(trimmed); h.Type != "" {
			flushPara()
			blocks = append(blocks, h)
			continue
		}

		// Blockquote: one or more consecutive "> " lines.
		if strings.HasPrefix(trimmed, ">") {
			flushPara()
			var q []string
			for i < len(lines) {
				t := strings.TrimSpace(lines[i])
				if !strings.HasPrefix(t, ">") {
					break
				}
				q = append(q, strings.TrimSpace(strings.TrimPrefix(t, ">")))
				i++
			}
			i--
			blocks = append(blocks, Block{Type: "quote", Text: strings.Join(q, "\n")})
			continue
		}

		// List: consecutive bullet or ordered items.
		if kind, _ := listItem(trimmed); kind != "" {
			flushPara()
			var items []string
			for i < len(lines) {
				t := strings.TrimSpace(lines[i])
				k, item := listItem(t)
				if k != kind {
					break
				}
				items = append(items, item)
				i++
			}
			i--
			blocks = append(blocks, Block{List: kind, Items: items})
			continue
		}

		para = append(para, ln)
	}
	flushPara()
	return blocks
}

// atxHeading parses "# Heading".."###### Heading" into a heading Block, or a zero
// Block when the line is not a heading.
func atxHeading(line string) Block {
	n := 0
	for n < len(line) && line[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || n >= len(line) || line[n] != ' ' {
		return Block{}
	}
	return Block{Type: "heading", Tag: "h" + string(rune('0'+n)), Text: strings.TrimSpace(line[n+1:])}
}

// listItem classifies a trimmed line as a bullet ("- "/"* "/"+ ") or number
// ("1. ") list row and returns the row's text, or "" when it is not a list item.
func listItem(line string) (kind, text string) {
	for _, p := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(line, p) {
			return "bullet", strings.TrimSpace(line[len(p):])
		}
	}
	// Ordered: digits then ". " or ") ".
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i > 0 && i+1 < len(line) && (line[i] == '.' || line[i] == ')') && line[i+1] == ' ' {
		return "number", strings.TrimSpace(line[i+2:])
	}
	return "", ""
}

// FromHTML parses an HTML fragment/document at block granularity, emitting a block
// per block-level element (h1-h6, p, blockquote, pre, li) in document order and
// grouping consecutive <li> under a list block. Inline text is collected verbatim
// with <br> as a newline; anchor text is kept (a normalizer that needs the href
// rewrites it to "[[Title]]" before calling this). Malformed HTML degrades to the
// text it can recover rather than failing.
func FromHTML(fragment string) string {
	doc, err := html.Parse(strings.NewReader(fragment))
	if err != nil {
		return FromPlain(fragment)
	}
	var blocks []Block
	walkHTML(doc, &blocks)
	if len(blocks) == 0 {
		return FromPlain(htmlText(doc))
	}
	return Build(blocks)
}

// walkHTML descends the node tree emitting a block when it meets a block-level
// element, and does not recurse into an emitted block (its inline text is captured
// whole) so text is never double-counted.
func walkHTML(n *html.Node, blocks *[]Block) {
	if n.Type == html.ElementNode {
		switch n.Data {
		case "script", "style", "head", "title":
			return
		case "h1", "h2", "h3", "h4", "h5", "h6":
			*blocks = append(*blocks, Block{Type: "heading", Tag: n.Data, Text: htmlText(n)})
			return
		case "p":
			if t := htmlText(n); strings.TrimSpace(t) != "" {
				*blocks = append(*blocks, Block{Type: "paragraph", Text: t})
			}
			return
		case "div", "section", "article":
			// A wrapper with nested block elements is a container — recurse. One that
			// holds only inline content is a line/paragraph (Evernote ENML emits a
			// <div> per line), so emit it as a paragraph.
			if hasBlockChild(n) {
				break
			}
			if t := htmlText(n); strings.TrimSpace(t) != "" {
				*blocks = append(*blocks, Block{Type: "paragraph", Text: t})
			}
			return
		case "blockquote":
			*blocks = append(*blocks, Block{Type: "quote", Text: htmlText(n)})
			return
		case "pre":
			*blocks = append(*blocks, Block{Type: "code", Text: htmlText(n)})
			return
		case "ul", "ol":
			kind := "bullet"
			if n.Data == "ol" {
				kind = "number"
			}
			var items []string
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				if c.Type == html.ElementNode && c.Data == "li" {
					items = append(items, htmlText(c))
				}
			}
			if len(items) > 0 {
				*blocks = append(*blocks, Block{List: kind, Items: items})
			}
			return
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walkHTML(c, blocks)
	}
}

// blockTags is the set walkHTML treats as block-level (used to decide whether a
// wrapper element is a container to recurse into or a line to emit).
var blockTags = map[string]bool{
	"div": true, "section": true, "article": true, "p": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"ul": true, "ol": true, "li": true, "blockquote": true, "pre": true, "table": true,
}

// hasBlockChild reports whether a subtree contains any block-level element, so a
// wrapper (<div>) with block children is recursed into rather than flattened.
func hasBlockChild(n *html.Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode && blockTags[c.Data] {
			return true
		}
		if hasBlockChild(c) {
			return true
		}
	}
	return false
}

// htmlText returns the concatenated text of a subtree, mapping <br> to a newline.
// Each text node's whitespace is collapsed while preserving a single boundary space
// so adjacent inline nodes (e.g. "<a>foo</a> bar") stay word-separated; a final
// pass collapses redundant spaces and trims each line.
func htmlText(n *html.Node) string {
	var b strings.Builder
	var rec func(*html.Node)
	rec = func(nd *html.Node) {
		if nd.Type == html.TextNode {
			b.WriteString(wsToSpace(nd.Data))
			return
		}
		if nd.Type == html.ElementNode && nd.Data == "br" {
			b.WriteString("\n")
			return
		}
		for c := nd.FirstChild; c != nil; c = c.NextSibling {
			rec(c)
		}
	}
	rec(n)
	return cleanInline(b.String())
}

// wsToSpace collapses each run of ASCII whitespace to a single space, keeping one
// leading/trailing space when the run is at a text node's boundary (so inter-node
// spacing survives concatenation).
func wsToSpace(s string) string {
	var b strings.Builder
	space := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case ' ', '\t', '\n', '\r', '\f':
			space = true
		default:
			if space {
				b.WriteByte(' ')
				space = false
			}
			b.WriteByte(c)
		}
	}
	if space {
		b.WriteByte(' ')
	}
	return b.String()
}

// cleanInline collapses redundant spaces within each line, drops spaces around the
// <br> newlines, and trims leading/trailing blank lines.
func cleanInline(s string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = strings.Join(strings.Fields(ln), " ")
	}
	return strings.Trim(strings.Join(lines, "\n"), "\n")
}
