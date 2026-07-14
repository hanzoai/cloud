// Package obsidian normalizes an Obsidian vault (a tree of markdown files) into
// vault.Page documents. It is pure (no cloud/framework/I/O): the KB import handler
// unpacks the vault zip into []vault.File and this package maps them to pages.
//
// Obsidian's model IS the wikilink graph, not a page hierarchy, so pages import
// flat (no parent tree) and "[[Wikilinks]]" are preserved verbatim in the body —
// the kb-page after_save hook then extracts them into kb-link edges, reconstructing
// the graph. YAML frontmatter is stripped from the body; a note's filename is its
// title (the link target Obsidian uses), so title-based link resolution matches
// what the author wrote.
package obsidian

import (
	"path"
	"strings"

	"github.com/hanzoai/cloud/clients/knowledge/lexical"
	"github.com/hanzoai/cloud/clients/knowledge/vault"
)

// Parse maps the markdown files of an Obsidian vault to pages. Non-markdown files
// (attachments) are ignored. The order of the returned pages follows the file list.
func Parse(files []vault.File) ([]vault.Page, error) {
	pages := make([]vault.Page, 0, len(files))
	for _, f := range files {
		if !strings.EqualFold(path.Ext(f.Path), ".md") {
			continue
		}
		title := noteName(f.Path)
		if title == "" {
			continue
		}
		body := stripFrontmatter(string(f.Data))
		pages = append(pages, vault.Page{
			Slug:  vault.Slug(title),
			Title: title,
			Body:  lexical.FromMarkdown(body),
		})
	}
	return pages, nil
}

// noteName is the vault-relative note name Obsidian wikilinks target: the filename
// without its .md extension (directory kept out — Obsidian resolves links by
// filename across the vault).
func noteName(p string) string {
	base := path.Base(p)
	return strings.TrimSuffix(base, path.Ext(base))
}

// stripFrontmatter removes a leading YAML frontmatter block ("---\n … \n---") from
// a note, returning the markdown body. A note without frontmatter is returned
// unchanged. Frontmatter metadata is intentionally not merged into the body (it is
// not prose); the note's filename remains the title.
func stripFrontmatter(md string) string {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	if !strings.HasPrefix(md, "---\n") {
		return strings.TrimSpace(md)
	}
	rest := md[len("---\n"):]
	// The closing fence is a line that is exactly "---".
	if i := strings.Index(rest, "\n---"); i >= 0 {
		after := rest[i+len("\n---"):]
		return strings.TrimSpace(strings.TrimPrefix(after, "\n"))
	}
	// Unterminated frontmatter — treat the whole thing as body rather than dropping it.
	return strings.TrimSpace(md)
}
