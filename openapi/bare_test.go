package openapi

import (
	"reflect"
	"testing"
)

// TestBareFindsWhatAnSDKReadsAndNothingElse pins the walk against the two ways a
// property hides: nested inside another property, and behind a composition
// keyword. A check that stopped at the top level would report a clean document
// while a whole sub-object shipped with nothing said about it — which is how the
// class went unmeasured for as long as it did.
func TestBareFindsWhatAnSDKReadsAndNothingElse(t *testing.T) {
	doc := &Document{
		OpenAPI: "3.1.0",
		Components: &Components{Schemas: map[string]any{
			"Row": map[string]any{"properties": map[string]any{
				"described": map[string]any{"type": "string", "description": "says something"},
				"blank":     map[string]any{"type": "string", "description": "   "},
				"missing":   map[string]any{"type": "integer"},
				"nested": map[string]any{
					"type":        "object",
					"description": "the object itself is described",
					"properties":  map[string]any{"inner": map[string]any{"type": "string"}},
				},
			}},
			"List": map[string]any{"type": "array", "items": map[string]any{
				"properties": map[string]any{"item": map[string]any{"type": "string"}},
			}},
			"Either": map[string]any{"oneOf": []any{
				map[string]any{"properties": map[string]any{"a": map[string]any{"type": "string"}}},
				map[string]any{"properties": map[string]any{"b": map[string]any{"type": "string", "description": "said"}}},
			}},
			"Scalar": map[string]any{"type": "string"},
		}},
	}
	got, err := Bare(doc)
	if err != nil {
		t.Fatalf("Bare: %v", err)
	}
	want := []string{
		"Either.a",
		"List[].item",
		"Row.blank",
		"Row.missing",
		"Row.nested.inner",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Bare() = %v, want %v", got, want)
	}
}

// TestBareOnADocumentWithNoComponents is the vacuous case: no schemas, no
// findings, no error. An app gates that separately (a subset publishing nothing
// would otherwise pass its own gate for the wrong reason), which is why this
// reports rather than refuses.
func TestBareOnADocumentWithNoComponents(t *testing.T) {
	got, err := Bare(&Document{OpenAPI: "3.1.0"})
	if err != nil {
		t.Fatalf("Bare: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Bare() = %v, want none", got)
	}
}
