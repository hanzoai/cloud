// Package vault is the normalized model an importer produces: a set of pages with
// a parent tree and wikilinks preserved inline in the body. It is pure (no cloud,
// no framework, no I/O) so every format normalizer — obsidian, notion, roam,
// evernote — maps to ONE shape that the KB import handler files as kb-page
// documents through the SAME framework.Ingest path a connector sync uses.
//
// A normalizer never writes anything: it turns raw export bytes into []Page, and
// the KB lane files them. Wikilinks stay as literal "[[Title]]" text in Body, so
// the kb-page after_save hook extracts them into kb-link edges exactly as it does
// for a hand-authored page — the import path adds no second link-extraction path.
package vault

import "strings"

// Page is a normalized knowledge page ready to file as a kb-page. Body is a Lexical
// EditorState JSON string (built via clients/knowledge/lexical) with wikilinks
// preserved as "[[Title]]" text. Parent is the slug of the parent page, or "" for a
// top-level page — the import handler files parents before children so the kb-page
// `parent` Link resolves.
type Page struct {
	Slug   string
	Title  string
	Body   string
	Parent string
}

// File is one entry from an unpacked archive: its archive path and raw bytes.
// Normalizers take a []File so they stay pure — the KB import handler owns the zip
// decode — and are unit-testable with a hand-built list, no real archive needed.
type File struct {
	Path string
	Data []byte
}

// Slug turns a page title/name into a URL-safe kb-page slug: lowercase, with every
// run of non-alphanumeric characters folded to a single dash and the ends trimmed.
// It is the ONE slug rule every normalizer shares, so the same title yields the
// same slug across formats. An empty result falls back to "page" (the import
// handler then makes it unique within the org).
func Slug(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(title)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
			continue
		}
		dash = true
	}
	if b.Len() == 0 {
		return "page"
	}
	return b.String()
}
