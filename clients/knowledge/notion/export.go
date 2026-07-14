// export.go normalizes a Notion workspace EXPORT (the "Markdown & CSV" or "HTML"
// zip a user downloads) into vault.Page documents — distinct from notion.go, which
// shapes records from the live Notion API connector. Both live in this package
// because both are pure Notion-format knowledge; neither depends on cloud.
//
// A Notion export encodes hierarchy in the directory tree (a page's subpages live
// in a folder named after the page) and links between pages as relative file links
// ([text](Child%20<id>.md) or <a href="Child%20<id>.html">). This normalizer
// rebuilds the parent tree from the folder nesting and rewrites intra-export links
// to "[[Title]]" wikilinks, so the kb-page after_save hook extracts them into
// kb-link edges — the export's link structure survives as real KB links.
package notion

import (
	"net/url"
	"path"
	"regexp"
	"strings"

	"github.com/hanzoai/cloud/clients/knowledge/lexical"
	"github.com/hanzoai/cloud/clients/knowledge/vault"
)

// notionID matches the 32-hex id Notion appends to every exported file/folder name
// (preceded by a space), so a display title can be recovered from the filename.
var notionID = regexp.MustCompile(` [0-9a-fA-F]{32}$`)

var (
	mdLinkRe   = regexp.MustCompile(`\[([^\]]*)\]\(([^)]*)\)`)
	htmlLinkRe = regexp.MustCompile(`(?is)<a\b[^>]*\bhref="([^"]*)"[^>]*>(.*?)</a>`)
	leadingH1  = regexp.MustCompile(`(?m)\A#\s+.*\n`)
)

// entry is one export page file with its derived identity, before link rewriting.
type entry struct {
	file   vault.File
	key    string // export-relative path without extension (e.g. "Parent id/Child id")
	dir    string // containing directory key ("" for a top-level page)
	title  string // filename with the Notion id stripped
	slug   string
	isHTML bool
}

// ParseExport maps a Notion export's page files (.md or .html) to pages, resolving
// the parent tree from folder nesting and rewriting intra-export links to
// wikilinks. Non-page files (.csv databases, images) are ignored.
func ParseExport(files []vault.File) ([]vault.Page, error) {
	entries := make([]entry, 0, len(files))
	byKey := map[string]*entry{}
	byBase := map[string]*entry{}

	for _, f := range files {
		ext := strings.ToLower(path.Ext(f.Path))
		if ext != ".md" && ext != ".html" {
			continue
		}
		key := strings.TrimSuffix(f.Path, path.Ext(f.Path))
		e := entry{
			file:   f,
			key:    key,
			dir:    dirKey(f.Path),
			title:  fileTitle(f.Path),
			slug:   vault.Slug(fileTitle(f.Path)),
			isHTML: ext == ".html",
		}
		entries = append(entries, e)
	}
	// Index by full key and by basename for link resolution (full key wins).
	for i := range entries {
		byKey[entries[i].key] = &entries[i]
		byBase[path.Base(entries[i].key)] = &entries[i]
	}

	pages := make([]vault.Page, 0, len(entries))
	for i := range entries {
		e := &entries[i]
		raw := string(e.file.Data)
		body := rewriteLinks(raw, e, byKey, byBase)

		var lex string
		if e.isHTML {
			lex = lexical.FromHTML(body)
		} else {
			lex = lexical.FromMarkdown(leadingH1.ReplaceAllString(body, ""))
		}
		parent := ""
		if p := byKey[e.dir]; p != nil {
			parent = p.slug
		}
		pages = append(pages, vault.Page{
			Slug:   e.slug,
			Title:  e.title,
			Body:   lex,
			Parent: parent,
		})
	}
	return pages, nil
}

// rewriteLinks replaces each intra-export link (markdown or HTML) whose target
// resolves to another export page with a "[[Title]]" wikilink, leaving external
// links (http/mailto/anchors) untouched.
func rewriteLinks(body string, from *entry, byKey, byBase map[string]*entry) string {
	repl := func(href string) (string, bool) {
		if t := resolve(from, href, byKey, byBase); t != nil {
			return "[[" + t.title + "]]", true
		}
		return "", false
	}
	if from.isHTML {
		return htmlLinkRe.ReplaceAllStringFunc(body, func(m string) string {
			sm := htmlLinkRe.FindStringSubmatch(m)
			if w, ok := repl(sm[1]); ok {
				return w
			}
			return m
		})
	}
	return mdLinkRe.ReplaceAllStringFunc(body, func(m string) string {
		sm := mdLinkRe.FindStringSubmatch(m)
		if w, ok := repl(sm[2]); ok {
			return w
		}
		return m
	})
}

// resolve maps a link href (relative to the linking file's directory) to the export
// page it targets, or nil for an external/unresolvable link.
func resolve(from *entry, href string, byKey, byBase map[string]*entry) *entry {
	href = strings.TrimSpace(href)
	if href == "" || strings.Contains(href, "://") || strings.HasPrefix(href, "#") || strings.HasPrefix(href, "mailto:") {
		return nil
	}
	dec, err := url.PathUnescape(href)
	if err != nil {
		dec = href
	}
	dec = strings.SplitN(dec, "#", 2)[0] // drop any heading anchor
	target := path.Clean(path.Join(path.Dir(from.file.Path), dec))
	key := strings.TrimSuffix(target, path.Ext(target))
	if e := byKey[key]; e != nil {
		return e
	}
	return byBase[path.Base(key)]
}

// fileTitle recovers a page's display title from its export filename: the basename
// without extension and without the trailing Notion id.
func fileTitle(p string) string {
	base := path.Base(p)
	base = strings.TrimSuffix(base, path.Ext(base))
	return strings.TrimSpace(notionID.ReplaceAllString(base, ""))
}

// dirKey is the containing-directory key of an export file ("" at the top level),
// used to find the parent page (whose own key equals this directory).
func dirKey(p string) string {
	d := path.Dir(p)
	if d == "." || d == "/" {
		return ""
	}
	return d
}
