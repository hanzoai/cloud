package lsp

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// prose_test.go gates the FIELD half of this surface. Typing a route documents its
// address and its shape; the shape's fields come from a different place — a doc
// comment on each one, which zipdoc lifts per field — so a fully typed plane can
// still publish a document nobody can read.
//
// It matters here more than most, because these numbers are a PROTOCOL's and not
// ours. `line` is 0-based and `character` counts UTF-16 code units, so an editor's
// own 1-based line is off by one and a line with an emoji in it is off by more.
// `kind` and `severity` are LSP enumerations passed through as integers — 12 is a
// function and 2 is a warning, and nothing but a sentence says so. `external` is
// the field the whole service exists for: when it is true the answer left the
// repository and `path` stopped being repo-relative. And `rev` is the RESOLVED
// commit, never the branch that was asked for, which is what makes an answer
// re-askable.
//
// The gate checks presence, not meaning. A description restating the field's name
// is worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(warm(t).app, openapi.Info{Title: "lsp", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("lsp publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/lsp describe",
			len(bare), strings.Join(bare, ", "))
	}
}
