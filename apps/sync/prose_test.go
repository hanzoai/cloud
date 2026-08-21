package sync

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// prose_test.go gates the FIELD half of this surface. Typing a route documents its
// address and its shape; the shape's fields come from a doc comment on each one,
// which zipdoc lifts per field — so a fully typed plane can still publish a document
// nobody can read.
//
// Two fields here decide what the engine DOES and neither says so by its name.
// `direction` includes "off", which keeps a link declared and moves nothing — the
// difference between a paused sync and a deleted one. `actor` is the loop guard: a
// reconcile writes as that identity, so a change made by it is one we already have
// and is not synced back; a caller who sets it to their own user is asking for their
// own edits to be ignored. And `updatedAt` is bumped by every reconcile, so it reads
// as the last-SYNCED time rather than the last edit.
//
// The gate checks presence, not meaning. A description restating the field's name is
// worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mountSyncUnderFlatten(t), openapi.Info{Title: "sync", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("sync publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/sync describe",
			len(bare), strings.Join(bare, ", "))
	}
}
