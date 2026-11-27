// Package lexical builds a Lexical EditorState JSON string from block-structured
// content. It is the ONE place a KB importer turns markdown / HTML / plain text
// into the kb.page `body` shape the console's Lexical editor renders and the KB
// indexer flattens (clients/knowledge.lexicalText is the inverse reader).
//
// It is pure (no cloud, no I/O). Inline content — including "[[wikilinks]]" — is
// carried VERBATIM as text so link extraction sees exactly what the author wrote;
// this package deliberately does NOT parse inline marks (bold/italic/inline links)
// into Lexical formatting, keeping the conversion honest at block granularity.
package lexical
