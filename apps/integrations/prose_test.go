package integrations

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
// It matters here because this surface is mostly SOMEBODY ELSE'S vocabulary
// relayed through ours, and a relayed word means what the upstream meant by it,
// not what it looks like. A search hit's `stars` is GitHub's stargazer count when
// the index answered, so it lags the repository; `private` is false for every hit
// because the index queried is the public one; `count` is the length of the array
// returned and NOT GitHub's total_count, so it never says how many more matched.
// And a hit's `full_name` is not a forkable name: githubFork takes a repository
// the org's installation was granted, which a public search result usually is not.
//
// Presence is all a gate can check. A description restating the field's name is
// worse than none, and only a reader catches that.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(newApp(t, newKMS(t)), openapi.Info{Title: "integrations", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("integrations publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/integrations describe",
			len(bare), strings.Join(bare, ", "))
	}
}
