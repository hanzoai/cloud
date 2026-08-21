package research

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
// This surface is a pile of unlabelled numbers without it. `value` carries no
// unit of its own — `metric` is the only place it is stated, and this plane never
// normalizes, so accuracy arrives as 0.81 from one producer and 94.3 from another
// and both are stored as sent. `cost_usd` is DOLLARS and not cents. `n` and
// `n_total` are a sample and the denominator the producer reports it against,
// neither derived from the other. `revision` and `status` are closed vocabularies
// that decide whether a run is counted at all — a retracted latest version
// withdraws its id, and faulted runs are retained but left out of every answered
// total, which is exactly why `experiments` and `experiments_retained` differ.
// And `visibility`, `trainable` and `publishable` are consent: an upload forces
// them and only a grant moves them, which a caller reading three bare booleans
// would never guess.
//
// Presence is all a gate can check. A description restating the field's name is
// worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mountResearch(t), openapi.Info{Title: "research", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("research publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/research describe",
			len(bare), strings.Join(bare, ", "))
	}
}
