package books

// prose_test.go gates the half of the surface projection_test.go cannot see. That file
// proves every /v1/books route reaches OpenAPI, MCP and the CLI under one operation id
// — it says nothing about whether the SHAPES those projections carry mean anything to
// a reader.
//
// On this surface they carry almost the whole product. Every amount here is int64
// CENTS, and a bare integer named `debit` or `amount` cannot tell an SDK user which of
// three different conventions it is under: the trial balance places a net on the column
// its SIGN chose, the P&L flips income once for display so both halves read positive,
// and a bank row is always positive with `direction` carrying the sign. Three sign
// rules, one Go type, and only prose separates them. `totalIncome` being ACCRUAL —
// recognized, so a prepaid top-up is not in it — is likewise a fact about the number
// that no schema can hold.
//
// The gate checks presence, not meaning: a description restating the field's name is
// worse than none, and only a reader catches that.

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// proseless is the CLOSED list of published components whose properties carry NO
// description, and it is a property of the SEAM they arrived through rather than of
// anyone's diligence.
//
// ScanDraft reaches the document only through openapi.Register (scan.go), because
// POST /v1/books/scan takes a PDF as its raw body and so cannot be a typed op. Register
// derives a schema by REFLECTION from the Go type, and Go drops comments at compile
// time — zipdoc, the pass that lifts field prose, walks zip's TYPED registrations and
// can therefore never reach a type that arrives this way. Its fields carry doc comments
// in the Go source like every other type in this package; reflection simply cannot see
// them. The choice at that route was a bare shape or NO shape, and a bare shape is what
// lets an SDK offer a receipt upload with a return type at all.
//
// Note what is NOT here, and why the ledger is one name rather than four: Extracted,
// LineItem and Question are nested inside ScanDraft AND reachable from a typed op
// (InboxItem carries an *Extracted, questions has its own read), so the typed side
// describes them and the document publishes one described copy.
//
// The list is exact in BOTH directions. A bare property anywhere else is a typed op's,
// which zipdoc can describe, and goes red. A component here that starts publishing
// prose also goes red — that is the day zip learns to lift comments through Register,
// and this ledger must shrink then rather than quietly outlive the limitation.
var proseless = map[string]bool{
	"ScanDraft": true,
}

// TestEveryPublishedFieldIsDescribed fails on any property of any published schema that
// carries no description and is not in the ledger above.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	app := mountBooks(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "books", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	// Read the MARSHALLED document, because that is the artifact. Components.Schemas is
	// open-typed — Register contributes a *Schema and the typed fold contributes zip's
	// own map — so walking the Go value would silently skip whichever half it did not
	// expect, and a gate that skips is a gate that passes for the wrong reason. The
	// JSON is what an SDK generator actually reads.
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal doc: %v", err)
	}
	var published struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &published); err != nil {
		t.Fatalf("decode doc: %v", err)
	}
	if len(published.Components.Schemas) == 0 {
		t.Fatal("books publishes no schemas at all — the gate would pass vacuously")
	}

	var bare []string
	for name, schema := range published.Components.Schemas {
		if proseless[name] {
			continue
		}
		var node any
		if err := json.Unmarshal(schema, &node); err != nil {
			t.Fatalf("decode schema %s: %v", name, err)
		}
		bare = append(bare, bareProperties(node, name)...)
	}
	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Write the field's OWN doc comment — a header above a group of fields is lifted "+
			"onto the first of them alone — then run: make -C apps/books describe",
			len(bare), strings.Join(bare, ", "))
	}
	// The ledger may not name a component the document no longer publishes, or the
	// exemption outlives the limitation that earned it.
	for name := range proseless {
		if _, ok := published.Components.Schemas[name]; !ok {
			t.Errorf("proseless names %q, which books no longer publishes — delete the entry", name)
		}
	}
}

// bareProperties walks one schema and returns the dotted paths of every property with
// no description. It descends into NESTED shapes too: an inline object inside a
// property is published exactly as an SDK reads it, so stopping at the top level would
// let a whole sub-object ship bare.
func bareProperties(node any, path string) []string {
	m, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	var bare []string
	if props, ok := m["properties"].(map[string]any); ok {
		for field, raw := range props {
			p, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if desc, _ := p["description"].(string); strings.TrimSpace(desc) == "" {
				bare = append(bare, path+"."+field)
			}
			bare = append(bare, bareProperties(p, path+"."+field)...)
		}
	}
	for _, key := range []string{"items", "additionalProperties"} {
		bare = append(bare, bareProperties(m[key], path+"[]")...)
	}
	for _, key := range []string{"allOf", "anyOf", "oneOf"} {
		if list, ok := m[key].([]any); ok {
			for _, alt := range list {
				bare = append(bare, bareProperties(alt, path)...)
			}
		}
	}
	return bare
}
