package guide

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// prose_test.go gates the half of the surface the op-level checks cannot see.
// Typing a route documents its ADDRESS and its SHAPE; it does not document the
// shape's FIELDS, and those come from a different place — doc comments on each
// struct field, which zipdoc lifts one field at a time.
//
// The distinction is not cosmetic here. This plane publishes an org's growth
// numbers, and two pairs of them are unreadable without prose: funnel.revenue is
// whatever currency the beacon stamped, in MAJOR units, while keyMetrics.revenueCents
// is the money of record in whole cents — an SDK user reading two numbers called
// "revenue" has no way to know they are different measurements of the same business.
// And every probe in `signals` reports FALSE when it could not be run, so "not
// observed" and "not there" are one value on the wire and only a sentence separates
// them.
//
// A description that restates the field name is worse than none, so this gate checks
// only for presence; the reviewer checks for meaning.

// TestEveryPublishedFieldIsDescribed fails on any property of any published schema
// that carries no description. It walks NESTED shapes too — an inline object inside
// a property is published just as an SDK reads it, so stopping at the top level
// would let a whole sub-object ship bare.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	app := newApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "guide", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	// Read the MARSHALLED document, because that is the artifact. Components.Schemas
	// is open-typed — openapi.Register contributes a *Schema and the typed fold
	// contributes zip's own map — so walking the Go value would silently skip
	// whichever half it did not expect, and a gate that skips is a gate that passes
	// for the wrong reason. The JSON is what an SDK generator actually reads.
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
		t.Fatal("guide publishes no schemas at all — the gate would pass vacuously")
	}

	var bare []string
	for name, schema := range published.Components.Schemas {
		var node any
		if err := json.Unmarshal(schema, &node); err != nil {
			t.Fatalf("decode schema %s: %v", name, err)
		}
		bare = append(bare, bareProperties(node, name)...)
	}

	if len(bare) > 0 {
		sort.Strings(bare)
		t.Errorf("published propert(ies) with no description: %s\n"+
			"Every field of a published schema is read by SDK users and by a model choosing a tool. "+
			"Write a doc comment on the struct field — its OWN comment, not a header above a group of "+
			"them, which zipdoc files under the first field alone — then run: "+
			"make -C apps/guide describe", strings.Join(bare, ", "))
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
