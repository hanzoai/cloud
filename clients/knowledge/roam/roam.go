// Package roam normalizes a Roam Research JSON export into vault.Page documents. It
// is pure (no cloud/framework/I/O). Roam pages are flat (top-level); each page is a
// tree of bullet blocks. This package flattens the block tree into an indented
// bullet list, preserving "[[wikilinks]]", "#tags", and "((block refs))" verbatim
// so the kb-page after_save hook extracts the links exactly as authored.
package roam

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/clients/knowledge/lexical"
	"github.com/hanzoai/cloud/clients/knowledge/vault"
)

// page is one entry of a Roam export. A page and a block share the same recursive
// shape (title/string + children), so one struct decodes both levels.
type page struct {
	Title    string  `json:"title"`
	String   string  `json:"string"`
	Children []block `json:"children"`
}

type block struct {
	String   string  `json:"string"`
	Children []block `json:"children"`
}

// Parse decodes a Roam JSON export (a top-level array of pages) into pages. It also
// tolerates a single-object export. A page's body is its block tree rendered as an
// indented bullet list.
func Parse(data []byte) ([]vault.Page, error) {
	trimmed := strings.TrimSpace(string(data))
	var roamPages []page
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal(data, &roamPages); err != nil {
			return nil, fmt.Errorf("roam: decode export: %w", err)
		}
	} else {
		var one page
		if err := json.Unmarshal(data, &one); err != nil {
			return nil, fmt.Errorf("roam: decode export: %w", err)
		}
		roamPages = []page{one}
	}

	out := make([]vault.Page, 0, len(roamPages))
	for _, p := range roamPages {
		title := strings.TrimSpace(p.Title)
		if title == "" {
			title = strings.TrimSpace(p.String)
		}
		if title == "" {
			continue
		}
		var b strings.Builder
		for _, ch := range p.Children {
			renderBlock(&b, ch, 0)
		}
		out = append(out, vault.Page{
			Slug:  vault.Slug(title),
			Title: title,
			Body:  lexical.FromMarkdown(b.String()),
		})
	}
	return out, nil
}

// renderBlock writes one block and its descendants as bullet lines, indenting by
// depth so the outline structure is legible; wikilink/tag/ref syntax is untouched.
func renderBlock(b *strings.Builder, blk block, depth int) {
	text := strings.TrimSpace(blk.String)
	if text != "" {
		b.WriteString(strings.Repeat("  ", depth))
		b.WriteString("- ")
		b.WriteString(text)
		b.WriteString("\n")
	}
	next := depth
	if text != "" {
		next = depth + 1
	}
	for _, ch := range blk.Children {
		renderBlock(b, ch, next)
	}
}
