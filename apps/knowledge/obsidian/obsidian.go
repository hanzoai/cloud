package obsidian

import (
	"path"
	"strings"

	"github.com/hanzoai/cloud/apps/knowledge/lexical"
	"github.com/hanzoai/cloud/apps/knowledge/vault"
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
	if _, after, ok := strings.Cut(rest, "\n---"); ok {
		after := after
		return strings.TrimSpace(strings.TrimPrefix(after, "\n"))
	}
	// Unterminated frontmatter — treat the whole thing as body rather than dropping it.
	return strings.TrimSpace(md)
}
