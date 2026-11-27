// Package vault is the normalized model an importer produces: a set of pages with
// a parent tree and wikilinks preserved inline in the body. It is pure (no cloud,
// no framework, no I/O) so every format normalizer — obsidian, notion, roam,
// evernote — maps to ONE shape that the KB import handler files as kb.page
// documents through the SAME framework.Ingest path a connector sync uses.
//
// A normalizer never writes anything: it turns raw export bytes into []Page, and
// the KB lane files them. Wikilinks stay as literal "[[Title]]" text in Body, so
// the kb.page after_save hook extracts them into kb.link edges exactly as it does
// for a hand-authored page — the import path adds no second link-extraction path.
package vault
