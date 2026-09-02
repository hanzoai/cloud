package projects

// prose_test.go gates the FIELD half of this surface. Typing a route documents its
// address and its shape; the shape's fields come from a different place — a doc
// comment on each one, which zipdoc lifts per field — so a fully typed plane can
// still publish a wholly unreadable document.
//
// It matters here because several fields on this surface are the difference between
// safe and unsafe use rather than between clear and unclear. An upload grant's
// `prefix` is the ONLY place it can write and its `fields` must be posted verbatim
// or the signature it is protected by simply fails; `expiresAt` is short and the
// grant is handed out exactly once, so a caller who plans to re-fetch it will find
// nothing. A domain's `records` are present only while a claim is pending, so their
// ABSENCE means nothing is owed rather than that we cannot say what is owed. And
// `slug` is not a label: it is the public hostname and the object-store key segment,
// which is why renaming a project does not move it.
//
// The gate checks presence, not meaning. A description that restates the field's name
// is worse than none, and only a reader catches that.

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// proseless is the CLOSED list of published properties that carry NO description
// because the CLIENT they arrived through cannot carry one — not because nobody wrote
// it. Both causes are generator limitations, both are recorded upstream in
// CLAUDE.md, and neither is worked around here: the workarounds available (writing a
// schema by hand, or unrolling a shape into copies) each replace one true statement
// with two that can drift.
//
// It is exact in BOTH directions. A bare property anywhere else goes red, and an
// entry here that starts publishing prose goes red too — that is the day the
// generator learns, and this ledger must shrink then rather than outlive the gap.
var proseless = map[string]bool{
	// REFLECTION CLIENT. GET /v1/projects/tags is declared with openapi.Register (tags.go)
	// because it answers a hosted page's tag config rather than a typed op's Out.
	// Register derives a schema by REFLECTION, and Go drops comments at compile
	// time, so zipdoc — which walks zip's TYPED registrations — can never reach a
	// type that arrives this way. These fields carry doc comments in the source;
	// reflection cannot see them.
	"browserTagOut.platform": true,
	"browserTagOut.type":     true,
	"browserTagOut.id":       true,
	"tagConfig.tags":         true,

	// ANONYMOUS STRUCT. `repo` on the two write bodies is an inline struct literal,
	// and zipdoc files a field's prose under the name of the type that DECLARES it —
	// which an anonymous struct does not have. The property itself IS described (the
	// comment on the `Repo` field lands); its two members cannot be. Naming the type
	// would fix it and would also change the published document, turning an inline
	// object into a $ref to a new component in every generated SDK — a shape change,
	// not a description, so it is a decision someone makes on purpose rather than a
	// side effect of writing prose. Same class as the embedded-struct defect.
	"projectsCreate.repo.url":    true,
	"projectsCreate.repo.branch": true,
	"projectsUpdate.repo.url":    true,
	"projectsUpdate.repo.branch": true,
}

// TestEveryPublishedFieldIsDescribed fails on any property of any published schema
// that carries no description and is not named above.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mountApp(t), openapi.Info{Title: "projects", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("projects publishes no schemas at all — the gate would pass vacuously")
	}
	published, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}

	var bare, stale []string
	seen := map[string]bool{}
	for _, path := range published {
		seen[path] = true
		if !proseless[path] {
			bare = append(bare, path)
		}
	}
	for path := range proseless {
		if !seen[path] {
			stale = append(stale, path)
		}
	}

	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted onto "+
			"the first of them alone — then run: make -C apps/projects describe",
			len(bare), strings.Join(bare, ", "))
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names propert(ies) that are gone or now described: %s\n"+
			"An exemption that outlives its cause is how a generator gap becomes permanent — "+
			"delete the entr(ies).", strings.Join(stale, ", "))
	}
}
