package openapi_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

func remainder(ops int, products map[string]int) openapi.Untyped {
	return openapi.Untyped{Operations: ops, Products: products}
}

// The whole point: a route added RAW is refused, and the message names the
// product and the delta rather than a total that says nothing about where.
func TestAGrownRemainderIsRefused(t *testing.T) {
	was := remainder(784, map[string]int{"agents": 6, "books": 5})
	_, err := was.Shrink(remainder(785, map[string]int{"agents": 7, "books": 5}))
	if err == nil {
		t.Fatal("a raw route was added and the ratchet allowed it — every gate here measures presence, " +
			"so nothing else would have noticed")
	}
	for _, want := range []string{"agents", "6", "7", "openapi/untyped.json", "untypedByDesign"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q, so it does not say where to look:\n%v", want, err)
		}
	}
}

// A product with NO raw operations growing one is the case worth saying out loud:
// it is a package that WAS fully dispatchable and stopped being.
func TestTheFirstRawRouteInAProductIsCalledOut(t *testing.T) {
	was := remainder(10, map[string]int{"agents": 10})
	_, err := was.Shrink(remainder(11, map[string]int{"agents": 10, "ask": 1}))
	if err == nil {
		t.Fatal("a fully-dispatchable product grew its first raw route and the ratchet allowed it")
	}
	if !strings.Contains(err.Error(), "had NO raw operations") {
		t.Errorf("the refusal reads like any other delta; this one deserves naming:\n%v", err)
	}
}

// A conversion tightens the ratchet with no hand edit — that is what makes the
// gate free to live with. It is the ONLY direction that is automatic.
func TestAConversionTightensIt(t *testing.T) {
	was := remainder(784, map[string]int{"agents": 11, "books": 5})
	got, err := was.Shrink(remainder(779, map[string]int{"agents": 6, "books": 5}))
	if err != nil {
		t.Fatalf("typing five routes was refused: %v", err)
	}
	if got.Operations != 779 || got.Products["agents"] != 6 {
		t.Errorf("the ratchet did not tighten: %+v", got)
	}
}

// A product that becomes fully dispatchable is DROPPED, not pinned at zero.
// Pinning would forbid it ever gaining a legitimate byte stream again — stricter
// than this ratchet means, and unsayable-yes.
func TestAClearedProductIsDroppedRatherThanPinnedAtZero(t *testing.T) {
	was := remainder(5, map[string]int{"agents": 5, "ask": 0})
	got, err := was.Shrink(remainder(0, map[string]int{}))
	if err != nil {
		t.Fatalf("clearing every raw route was refused: %v", err)
	}
	if _, pinned := got.Products["agents"]; pinned {
		t.Error("a cleared product was pinned at zero, which forbids it ever gaining a byte stream")
	}
	if len(got.Products) != 0 {
		t.Errorf("want no products, got %+v", got.Products)
	}
}

// A DELIBERATE new raw route is let through by a hand edit, and that one edit must
// not be undone by the next unrelated regeneration — the mirror of the floor's
// pointwise-max argument.
func TestAHandRaisedNumberSurvivesTheNextRegeneration(t *testing.T) {
	was := remainder(100, map[string]int{"a": 7, "b": 9})
	got, err := was.Shrink(remainder(100, map[string]int{"a": 7, "b": 9}))
	if err != nil {
		t.Fatalf("an unchanged regeneration was refused: %v", err)
	}
	if got.Products["a"] != 7 || got.Products["b"] != 9 {
		t.Errorf("the hand-raised numbers moved: %+v", got.Products)
	}
}

// ReadUntyped refuses a missing file for ReadFloor's reason: an absent ratchet
// makes every regression legal, so losing the file must not lose the guarantee.
func TestReadUntypedRefusesAMissingFile(t *testing.T) {
	if _, err := openapi.ReadUntyped(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("a missing ratchet was treated as zero — every regeneration would then be allowed to " +
			"publish more that no client can call")
	}
}

// Count reads x-tool and nothing else, so it asks the same question the MCP
// catalogue asks. An operation OUTSIDE /v1 carries no tag and is counted in the
// total and in no product, which is exactly what it is.
func TestCountReadsTheDispatchableMarkAndTagsIndependently(t *testing.T) {
	d := &openapi.Document{Paths: map[string]openapi.PathItem{
		"/v1/agent/sessions": {"get": &openapi.Operation{Tool: true, Tags: []string{"agents"}}},
		"/v1/agent/stream":   {"get": &openapi.Operation{Tags: []string{"agents"}}},
		"/.well-known/x":      {"get": &openapi.Operation{}},
	}}
	got := openapi.Count(d)
	if got.Operations != 2 {
		t.Errorf("Operations = %d, want 2 (the typed one is dispatchable; the untagged one still counts)", got.Operations)
	}
	if got.Products["agents"] != 1 {
		t.Errorf("agents = %d, want 1 — the typed op must not be counted against its product", got.Products["agents"])
	}
	if len(got.Products) != 1 {
		t.Errorf("an operation outside /v1 has no product and must land in none: %+v", got.Products)
	}
}
