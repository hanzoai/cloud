package guide

import (
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
// that carries no description. [openapi.Bare] walks NESTED shapes too — an inline
// object inside a property is published just as an SDK reads it, so stopping at the
// top level would let a whole sub-object ship bare.
func TestEveryPublishedFieldIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(newApp(t), openapi.Info{Title: "guide", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	if doc.Components == nil || len(doc.Components.Schemas) == 0 {
		t.Fatal("guide publishes no schemas at all — the gate would pass vacuously")
	}
	bare, err := openapi.Bare(doc)
	if err != nil {
		t.Fatalf("bare: %v", err)
	}
	if len(bare) > 0 {
		t.Errorf("%d published schema propert(ies) with no description: %s\n"+
			"Every field of a published schema is read by SDK users and by a model choosing a tool. "+
			"Write a doc comment on the struct field — its OWN comment, not a header above a group of "+
			"them, which zipdoc files under the first field alone — then run: "+
			"make -C apps/guide describe", len(bare), strings.Join(bare, ", "))
	}
}
