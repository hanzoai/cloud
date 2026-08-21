package sandbox

// prose_test.go gates the FIELD half of this surface. Typing a route documents its
// ADDRESS and its SHAPE; the shape's FIELDS come from a different place — a doc
// comment on each one, which zipdoc lifts one at a time — so a fully typed surface
// can still publish a document nobody can act on.
//
// It matters here because every value on this surface is a fact about somebody
// else's computer. `class` is a closed vocabulary (exec | dev | desktop | android)
// and picking the wrong one silently spends a lease on a sandbox that keeps nothing;
// `workdir` is where a relative path resolves and differs BY class, so a caller that
// assumed one holds a second copy of a fact only the sandbox knows; `exitCode` is the
// program's answer and not the call's, so a caller that treated non-zero as a failed
// request would report our outage for their failing tests; and `stopped` counting
// zero means the sandbox was idle, not that the stop was refused.
//
// The gate checks presence, not meaning. A description restating the field's name is
// worse than none, and only a reader catches that.

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// TestEveryPublishedFieldIsDescribed fails on any property of any published schema
// that carries no description.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mountHTTP(t), openapi.Info{Title: "sandbox", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("sandboxes publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/sandbox describe",
			len(bare), strings.Join(bare, ", "))
	}
}
