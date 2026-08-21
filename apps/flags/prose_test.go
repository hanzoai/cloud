package flags

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// TestEveryPublishedFieldIsDescribed closes the half of the surface an op-level
// gate cannot see. Typing a route documents its ADDRESS and its SHAPE; the
// shape's FIELDS come from a different place — a doc comment on each one, which
// zipdoc lifts one at a time.
//
// It earns its place here because a flag definition is a document this package
// stores and does not interpret. `definition` is kept byte-for-byte and carries
// fields no Go type here names, so a caller that rebuilds it from the parts it
// recognizes silently drops targeting groups and payloads; `version` counts
// WRITES, not changes; `updated_by` and `actor` are empty for an in-process
// composer, which is not the same as unknown; and `detail` is a column nothing
// writes yet, so its absence says nothing about the change it sits beside.
//
// Presence is all a gate can check. A description restating the field's name is
// worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mountHTTP(t), openapi.Info{Title: "flags", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("flags publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/flags describe",
			len(bare), strings.Join(bare, ", "))
	}
}
