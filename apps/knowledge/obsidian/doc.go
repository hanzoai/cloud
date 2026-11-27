// Package obsidian normalizes an Obsidian vault (a tree of markdown files) into
// vault.Page documents. It is pure (no cloud/framework/I/O): the KB import handler
// unpacks the vault zip into []vault.File and this package maps them to pages.
//
// Obsidian's model IS the wikilink graph, not a page hierarchy, so pages import
// flat (no parent tree) and "[[Wikilinks]]" are preserved verbatim in the body —
// the kb.page after_save hook then extracts them into kb.link edges, reconstructing
// the graph. YAML frontmatter is stripped from the body; a note's filename is its
// title (the link target Obsidian uses), so title-based link resolution matches
// what the author wrote.
package obsidian
