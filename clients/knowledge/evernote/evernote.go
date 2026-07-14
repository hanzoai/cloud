// Package evernote normalizes an Evernote .enex export into vault.Page documents.
// It is pure (no cloud/framework/I/O). An .enex file is XML: a sequence of <note>
// elements whose <content> is ENML (an XHTML dialect). Notes import flat (Evernote
// has no note hierarchy); each note's ENML body is converted to Lexical at block
// granularity via clients/knowledge/lexical.
package evernote

import (
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud/clients/knowledge/lexical"
	"github.com/hanzoai/cloud/clients/knowledge/vault"
)

// export mirrors the .enex root. Only the fields the KB import needs are decoded;
// resources (attachments), tags, and timestamps are ignored.
type export struct {
	Notes []struct {
		Title   string `xml:"title"`
		Content string `xml:"content"` // ENML XHTML, usually CDATA-wrapped
	} `xml:"note"`
}

// Parse decodes an .enex document into pages, one per <note>. The ENML content is
// rendered from its inner en-note body; a note with no title is skipped (Evernote
// requires a title, but a malformed export should not produce a bogus page).
func Parse(data []byte) ([]vault.Page, error) {
	var ex export
	if err := xml.Unmarshal(data, &ex); err != nil {
		return nil, fmt.Errorf("evernote: decode enex: %w", err)
	}
	out := make([]vault.Page, 0, len(ex.Notes))
	for _, n := range ex.Notes {
		title := strings.TrimSpace(n.Title)
		if title == "" {
			continue
		}
		out = append(out, vault.Page{
			Slug:  vault.Slug(title),
			Title: title,
			Body:  lexical.FromHTML(enmlBody(n.Content)),
		})
	}
	return out, nil
}

// enmlBody returns the inner markup of the ENML <en-note> wrapper (its own XML
// declaration and DOCTYPE stripped), so the HTML converter sees just the content.
// A payload without an <en-note> wrapper is returned as-is (the converter tolerates
// a bare fragment).
func enmlBody(content string) string {
	c := strings.TrimSpace(content)
	if i := strings.Index(c, "<en-note"); i >= 0 {
		if j := strings.IndexByte(c[i:], '>'); j >= 0 {
			c = c[i+j+1:]
		}
	}
	if k := strings.LastIndex(c, "</en-note>"); k >= 0 {
		c = c[:k]
	}
	return c
}
