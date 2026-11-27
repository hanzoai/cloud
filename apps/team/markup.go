package team

// markup.go is the ONE place message markup is written and read.
//
// THE STORED FORM IS THE PLATFORM'S: a ProseMirror document serialized as JSON
// (text-core jsonToMarkup) —
//
//	{"type":"doc","content":[{"type":"paragraph","content":[
//	  {"type":"text","text":"ping "},
//	  {"type":"reference","attrs":{"id":"person-<uuid>","objectclass":"contact:class:Person","label":"Ada"}}]}]}
//
// The Team client parses stored markup with markupToJSON, which treats anything
// not starting with "{" as PLAIN TEXT — so HTML written here would render as
// literal tags, and a mention written as an HTML span would render as text and
// notify nobody the client can see. Every writer here therefore writes JSON.
//
// Rows written before that, by earlier versions of this service, hold HTML. The
// readers accept both, and read a mention in either form ONLY from a real
// reference node — a JSON node of type "reference", or an HTML span carrying
// data-type="reference" — never from a `person-<uuid>` that merely appears in
// the text, which anybody can type.

import (
	"encoding/json"
	"html"
	"regexp"
	"strings"
)

// A mention is written `<@uuid>` in typed text.
var mentionToken = regexp.MustCompile(`<@(` + uuidRe.String() + `)>`)

// mnode is one ProseMirror node, as the platform serializes it.
type mnode struct {
	Type    string         `json:"type"`
	Text    string         `json:"text,omitempty"`
	Attrs   map[string]any `json:"attrs,omitempty"`
	Content []mnode        `json:"content,omitempty"`
}

// textMarkup renders typed text as stored markup: each non-empty line a
// paragraph, and each `<@uuid>` naming somebody in `names` a reference node
// labelled with their name. A token naming nobody the space knows stays literal
// text — a mention of a stranger is a string, not a notification to an
// arbitrary uuid. Text is never parsed for tags: "a <b> c" is what was typed.
func textMarkup(text string, names map[string]string) string {
	var paras []mnode
	for _, ln := range lines(text) {
		var inline []mnode
		last := 0
		for _, m := range mentionToken.FindAllStringSubmatchIndex(ln, -1) {
			uid := ln[m[2]:m[3]]
			name, known := names[uid]
			if !known {
				continue
			}
			if m[0] > last {
				inline = append(inline, mnode{Type: "text", Text: ln[last:m[0]]})
			}
			inline = append(inline, referenceNode(uid, name))
			last = m[1]
		}
		if last < len(ln) {
			inline = append(inline, mnode{Type: "text", Text: ln[last:]})
		}
		paras = append(paras, mnode{Type: "paragraph", Content: inline})
	}
	return docMarkup(paras)
}

// answerMarkup renders an agent's answer as stored markup. Models often answer
// in HTML-ish markup (`<p>yes.</p>`), so the answer is flattened to plain text
// first — its block boundaries becoming paragraphs — and never rendered as
// markup of its own: whatever the model wrote is text in a paragraph.
func answerMarkup(answer string) string {
	var paras []mnode
	for _, ln := range lines(flattenMarkup(answer)) {
		paras = append(paras, mnode{Type: "paragraph", Content: []mnode{{Type: "text", Text: ln}}})
	}
	return docMarkup(paras)
}

// docMarkup serializes paragraphs as a doc, never an empty content list — the
// platform's empty document is one empty paragraph.
func docMarkup(paras []mnode) string {
	if len(paras) == 0 {
		paras = []mnode{{Type: "paragraph"}}
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(mnode{Type: "doc", Content: paras})
	return strings.TrimSpace(b.String())
}

// lines is text's non-empty lines, right-trimmed.
func lines(text string) []string {
	var out []string
	for _, ln := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if ln = strings.TrimRight(ln, " \t"); strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}

// referenceNode is the platform's mention of one person.
func referenceNode(uid, name string) mnode {
	return mnode{Type: "reference", Attrs: map[string]any{
		"id": PersonRef(uid), "objectclass": clPerson, "label": pick(strings.TrimSpace(name), uid),
	}}
}

// parsed decodes stored JSON markup, reporting false for anything else (the
// HTML rows written before this service wrote JSON).
func parsed(markup string) (mnode, bool) {
	if !strings.HasPrefix(strings.TrimSpace(markup), "{") {
		return mnode{}, false
	}
	var doc mnode
	if json.Unmarshal([]byte(markup), &doc) != nil {
		return mnode{}, false
	}
	return doc, true
}

// walk visits every node depth-first.
func (n mnode) walk(visit func(mnode)) {
	visit(n)
	for _, c := range n.Content {
		c.walk(visit)
	}
}

// Block nodes whose end separates words.
var blockNodes = map[string]bool{
	"paragraph": true, "heading": true, "blockquote": true, "code_block": true,
	"list_item": true, "hard_break": true, "table_cell": true,
}

var (
	tagRe        = regexp.MustCompile(`<[^>]*>`)
	blockTagRe   = regexp.MustCompile(`(?i)<(/p|br|/div|/li)\s*/?>`)
	spaceRunRe   = regexp.MustCompile(`\s+`)
	answerBlocks = regexp.MustCompile(`(?i)</(p|div|li|h[1-6])>|<br\s*/?>`)
	newlineRunRe = regexp.MustCompile(`\n{2,}`)
)

// plainText renders stored markup as the plain text a reader (or an agent's
// prompt) should see: text nodes in order, a mention as "@Label", block
// boundaries as spaces, whitespace collapsed. JSON markup is walked; a legacy
// HTML row has its tags dropped and entities unescaped.
func plainText(markup string) string {
	if doc, ok := parsed(markup); ok {
		var b strings.Builder
		var emit func(n mnode)
		emit = func(n mnode) {
			switch n.Type {
			case "text":
				b.WriteString(n.Text)
			case "reference":
				b.WriteString("@" + str(n.Attrs["label"]))
			}
			for _, c := range n.Content {
				emit(c)
			}
			if blockNodes[n.Type] {
				b.WriteString(" ")
			}
		}
		emit(doc)
		return strings.TrimSpace(spaceRunRe.ReplaceAllString(b.String(), " "))
	}
	s := blockTagRe.ReplaceAllString(markup, " ")
	s = tagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	return strings.TrimSpace(spaceRunRe.ReplaceAllString(s, " "))
}

// flattenMarkup normalizes an agent answer to PLAIN text: block boundaries
// become newlines, remaining tags are stripped, entities unescaped. Idempotent on
// genuine plain text.
func flattenMarkup(text string) string {
	s := answerBlocks.ReplaceAllString(text, "\n")
	s = tagRe.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = newlineRunRe.ReplaceAllString(s, "\n")
	return strings.TrimSpace(s)
}

// htmlSpanRe is one opening span tag, and the two attributes a legacy HTML
// reference node carries.
var (
	htmlSpanRe  = regexp.MustCompile(`(?i)<span\b[^>]*>`)
	htmlTypeRe  = regexp.MustCompile(`(?i)\bdata-type\s*=\s*"reference"`)
	htmlDataIDs = regexp.MustCompile(`(?i)\bdata-id\s*=\s*"person-(` + uuidRe.String() + `)"`)
	personIDRe  = regexp.MustCompile(`^person-(` + uuidRe.String() + `)$`)
)

// mentionedAccounts lists every distinct account uuid a stored message
// mentions, in order — read ONLY from reference nodes. It is what notify.go
// files "mentioned you" by and what wakes an agent, so text that merely contains
// a person id mentions nobody.
func mentionedAccounts(markup string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(uid string) {
		if uid != "" && !seen[uid] {
			seen[uid] = true
			out = append(out, uid)
		}
	}
	if doc, ok := parsed(markup); ok {
		doc.walk(func(n mnode) {
			if n.Type != "reference" {
				return
			}
			if m := personIDRe.FindStringSubmatch(str(n.Attrs["id"])); m != nil {
				add(m[1])
			}
		})
		return out
	}
	for _, tag := range htmlSpanRe.FindAllString(markup, -1) {
		if !htmlTypeRe.MatchString(tag) {
			continue
		}
		if m := htmlDataIDs.FindStringSubmatch(tag); m != nil {
			add(m[1])
		}
	}
	return out
}

// mentionsIn is mentionedAccounts, never nil, for a published list.
func mentionsIn(markup string) []string {
	out := mentionedAccounts(markup)
	if out == nil {
		out = []string{}
	}
	return out
}

// mentionsBot reports whether a stored message mentions the bot member.
func mentionsBot(markup, uid string) bool {
	if uid == "" {
		return false
	}
	for _, m := range mentionedAccounts(markup) {
		if m == uid {
			return true
		}
	}
	return false
}

// mentionTokens lists the distinct account uuids a typed text's `<@uuid>`
// tokens name, so the caller resolves names only when there is somebody to name.
func mentionTokens(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range mentionToken.FindAllStringSubmatch(text, -1) {
		uid := m[1]
		if !seen[uid] {
			seen[uid] = true
			out = append(out, uid)
		}
	}
	return out
}
