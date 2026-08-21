package marketplace

// prose_test.go gates the FIELD half of this surface. Typing a route documents its
// address and its shape; the shape's fields come from a different place — a doc
// comment on each one, which zipdoc lifts per field — so a fully typed plane can
// still publish a wholly unreadable document.
//
// It is a shop, so the unreadable parts are the ones that cost money. `price` is an
// EXACT 18-decimal magnitude, not cents, which is the whole reason a $0.0025 per-call
// price survives being published; `currency` rides along as a LABEL, since publish
// parses USD and the x402 terms carry no currency at all. `public` is not a
// visibility flag: only public rows reach the price table, so a private listing with
// a price charges nobody. And `installed` is per CALLER — the same listing reads
// installed for one org and not for another.
//
// The gate checks presence, not meaning. A description restating the field's name is
// worse than none, and only a reader catches that.

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// proseless is the CLOSED list of published properties that carry NO description
// because the SEAM they arrived through cannot carry one — not because nobody wrote
// it. Every one of them is a field of tools.Tool, which marketItem EMBEDS
// (marketplace.go), and each carries a doc comment on its own declaration
// (apps/tools/tool.go). zipdoc files a field's prose under the type that DECLARES
// it, so the lift lands on "Tool.activated" and the document publishes
// "marketItem.activated" — a name no describer ever names. `price` is doubly out of
// reach: it publishes as a bare $ref, beside which a description is ignored anyway.
//
// The workarounds are worse than the gap. Unrolling the embedding into copies, or
// hand-writing a schema beside the struct, each replace one true statement with two
// that can drift; naming an inline shape would change the published document rather
// than describe it. So the comment stays on tools.Tool's own fields, where it is
// already correct, and this records what cannot reach the wire from there.
//
// It is exact in BOTH directions. A bare property anywhere else goes red, and an
// entry here that starts publishing prose goes red too — that is the day zipdoc
// learns to follow an embedding, and this ledger must shrink then rather than
// outlive the gap.
var proseless = map[string]bool{
	"marketItem.activated":    true,
	"marketItem.description":  true,
	"marketItem.dispatchable": true,
	"marketItem.inputSchema":  true,
	"marketItem.name":         true,
	"marketItem.price":        true,
	"marketItem.source":       true,
}

// TestEveryPublishedFieldIsDescribed fails on any property of any published schema
// that carries no description and is not named above.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(setup(t), openapi.Info{Title: "marketplace", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("marketplace publishes no schemas at all — the gate would pass vacuously")
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
			"the first of them alone — then run: make -C apps/marketplace describe",
			len(bare), strings.Join(bare, ", "))
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("proseless names propert(ies) that are gone or now described: %s\n"+
			"An exemption that outlives its cause is how a generator gap becomes permanent — "+
			"delete the entr(ies).", strings.Join(stale, ", "))
	}
}
