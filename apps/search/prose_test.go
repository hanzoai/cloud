package search

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// TestEveryPublishedFieldIsDescribed closes the half of the surface an op-level
// gate cannot see. Typing a route documents its ADDRESS and its SHAPE; the
// shape's FIELDS come from a different place — a doc comment on each one, which
// zipdoc lifts one at a time. This surface is almost entirely numbers whose scale
// is the answer: `hits[].score` is a RANK artifact (RRF sums 1/(60+rank), so two
// agreeing legs top out near 0.033) and is comparable only within ONE response,
// while `matched[].score` is each leg's own native scale — a cosine similarity
// from the vector leg, and 0 from the lexical one meaning UNSCORED, not scored
// zero. `backends[].status` is a closed four-word vocabulary where only
// `degraded` is a fault, and the two `took_ms` differ: a leg's is its own call,
// the response's is the query including fusion.
//
// Presence is all a gate can check. A description restating the field's name is
// worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mount(t), openapi.Info{Title: "search", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("search publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/search describe",
			len(bare), strings.Join(bare, ", "))
	}
}
