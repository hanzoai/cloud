package openapi_test

// The ratchet, tested on the shape of the failure it exists to catch: a
// regeneration that publishes LESS than the last one and says nothing about it.

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

func floor(paths, ops int, products map[string]int) openapi.Floor {
	return openapi.Floor{Paths: paths, Operations: ops, Products: products}
}

func TestFloorRefusesAProductThatShrank(t *testing.T) {
	was := floor(1244, 1819, map[string]int{"iam": 161, "chat": 2})
	_, err := was.Raise(floor(1090, 1660, map[string]int{"iam": 4, "chat": 2}))
	if err == nil {
		t.Fatal("Raise accepted a surface that lost 157 of iam's paths — a smaller document reads exactly like a smaller API")
	}
	for _, want := range []string{"iam", "161", "4", "openapi/floor.json"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message does not name %q:\n%s", want, err)
		}
	}
}

// A product that vanishes entirely is the case that cost 46 products, so the
// message says so rather than reporting it as one more row of arithmetic.
func TestFloorNamesAProductThatVanished(t *testing.T) {
	was := floor(10, 20, map[string]int{"sentry": 5})
	_, err := was.Raise(floor(10, 20, map[string]int{}))
	if err == nil || !strings.Contains(err.Error(), "the whole product") {
		t.Fatalf("Raise = %v, want a refusal that says the product is gone", err)
	}
}

// Growth is the normal case and raises the floor to what was just published.
func TestFloorRaisesOnGrowth(t *testing.T) {
	was := floor(1058, 1491, map[string]int{"iam": 18})
	got, err := was.Raise(floor(1244, 1819, map[string]int{"iam": 18, "chat": 2}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Paths != 1244 || got.Operations != 1819 || got.Products["chat"] != 2 {
		t.Errorf("Raise = %+v, want the new counts", got)
	}
}

// The floor is the pointwise MAXIMUM, not the new measurement. A deliberate
// deletion is let through by editing floor.json, and that one edit must not be
// silently undone by the next unrelated regeneration — nor may an unrelated
// regeneration quietly ratchet a hand-lowered product back up past what it serves.
func TestFloorKeepsTheHigherOfEachProduct(t *testing.T) {
	was := floor(100, 200, map[string]int{"a": 5, "b": 9})
	got, err := was.Raise(floor(100, 200, map[string]int{"a": 7, "b": 9}))
	if err != nil {
		t.Fatal(err)
	}
	if got.Products["a"] != 7 || got.Products["b"] != 9 {
		t.Errorf("Raise = %+v, want the pointwise max", got.Products)
	}
}

// An absent floor makes every shrink legal, so losing the file must not be a way
// to lose the guarantee.
func TestReadFloorRefusesAMissingFile(t *testing.T) {
	if _, err := openapi.ReadFloor(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("ReadFloor treated a missing floor as zero — every regeneration would then be allowed to publish less")
	}
}

// Measure counts products off the OPERATIONS, never off the document's own tag
// list: the tag list is itself derived, and a bug that emptied it would make every
// product vanish from the floor at once.
func TestMeasureCountsProductsOffTheOperations(t *testing.T) {
	d := &openapi.Document{
		Tags: []openapi.Tag{{Name: "ghost"}},
		Paths: map[string]openapi.PathItem{
			"/v1/chat/completions": {"post": {OperationID: "a", Tags: []string{"chat"}}},
			"/healthz":             {"get": {OperationID: "b"}},
		},
	}
	f := openapi.Measure(d)
	if f.Paths != 2 || f.Operations != 2 {
		t.Errorf("Measure = %+v, want 2 paths / 2 operations", f)
	}
	if f.Products["chat"] != 1 || len(f.Products) != 1 {
		t.Errorf("products = %v, want exactly {chat:1} — an untagged operation belongs to no product and a tag with no operation is not one", f.Products)
	}
}
