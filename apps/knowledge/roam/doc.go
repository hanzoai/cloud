// Package roam normalizes a Roam Research JSON export into vault.Page documents. It
// is pure (no cloud/framework/I/O). Roam pages are flat (top-level); each page is a
// tree of bullet blocks. This package flattens the block tree into an indented
// bullet list, preserving "[[wikilinks]]", "#tags", and "((block refs))" verbatim
// so the kb.page after_save hook extracts the links exactly as authored.
package roam
