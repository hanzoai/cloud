package prefs

import (
	"encoding/json"
	"strings"
	"testing"
)

// The merge is SHALLOW and key-wise: a surface saves only the keys it owns, and
// every other surface's keys survive. This is the whole reason the endpoint is a
// PATCH — if a save dropped keys the client didn't know about, two products
// sharing one document would erase each other on every write.
func TestMergeDoc_PreservesForeignKeys(t *testing.T) {
	// The console saves `theme`; insights' `density` must survive untouched.
	got, err := mergeDoc(`{"density":"compact","theme":"light"}`, map[string]any{"theme": "dark"})
	if err != nil {
		t.Fatalf("mergeDoc: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("result is not JSON: %v", err)
	}
	if doc["theme"] != "dark" {
		t.Fatalf("patched key not applied: %v", doc["theme"])
	}
	if doc["density"] != "compact" {
		t.Fatalf("foreign key was dropped by a partial save: %v", doc["density"])
	}
}

// A null VALUE deletes its key. Without this a preference could be set but never
// cleared, and every client would invent its own "unset" sentinel.
func TestMergeDoc_NullDeletesKey(t *testing.T) {
	got, err := mergeDoc(`{"theme":"dark","pinned":["a"]}`, map[string]any{"theme": nil})
	if err != nil {
		t.Fatalf("mergeDoc: %v", err)
	}
	var doc map[string]any
	_ = json.Unmarshal([]byte(got), &doc)
	if _, present := doc["theme"]; present {
		t.Fatalf("null did not delete the key: %s", got)
	}
	if _, present := doc["pinned"]; !present {
		t.Fatalf("null deleted an unrelated key: %s", got)
	}
}

// Shallow, not deep: a client must be able to REPLACE a nested object outright.
// A deep merge would make a nested value append-only — you could add sub-keys
// forever but never remove one.
func TestMergeDoc_ReplacesNestedObjectWholesale(t *testing.T) {
	got, err := mergeDoc(`{"nav":{"a":1,"b":2}}`, map[string]any{"nav": map[string]any{"c": 3}})
	if err != nil {
		t.Fatalf("mergeDoc: %v", err)
	}
	var doc struct {
		Nav map[string]any `json:"nav"`
	}
	_ = json.Unmarshal([]byte(got), &doc)
	if _, stale := doc.Nav["a"]; stale {
		t.Fatalf("nested object was deep-merged, not replaced: %s", got)
	}
	if doc.Nav["c"] != float64(3) {
		t.Fatalf("replacement value missing: %s", got)
	}
}

// A corrupt stored row starts the user fresh rather than failing every future
// write. Preferences are not a system of record; refusing to save a theme
// forever because one row got mangled is the worse failure.
func TestMergeDoc_CorruptStoredDocStartsFresh(t *testing.T) {
	for _, stored := range []string{"", "not json", "[]", "null", "42"} {
		got, err := mergeDoc(stored, map[string]any{"theme": "dark"})
		if err != nil {
			t.Fatalf("stored=%q: mergeDoc: %v", stored, err)
		}
		if !strings.Contains(got, `"theme":"dark"`) {
			t.Fatalf("stored=%q: patch not applied: %s", stored, got)
		}
	}
}

// The document bound is enforced on the MERGED result, not just the patch — many
// small accepted patches must not add up to an unbounded row.
func TestMergeDoc_BoundsMergedResult(t *testing.T) {
	big := map[string]any{"blob": strings.Repeat("x", maxDoc)}
	if _, err := mergeDoc(`{}`, big); err == nil {
		t.Fatal("an over-sized merged document was accepted")
	}
	full := map[string]any{}
	for i := 0; i < maxKeys; i++ {
		full[string(rune('a'+i%26))+string(rune('a'+i/26))] = 1
	}
	stored, err := mergeDoc(`{}`, full)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := mergeDoc(stored, map[string]any{"one-key-too-many": 1}); err == nil {
		t.Fatal("exceeding the key bound via an incremental patch was accepted")
	}
}

// The body must be a JSON OBJECT. `null` unmarshals into a nil map WITHOUT error,
// so it needs an explicit refusal — otherwise it silently reads as an empty patch
// and returns 200 on a request that saved nothing.
func TestDecodePatch_Refusals(t *testing.T) {
	cases := []struct{ name, body string }{
		{"empty", ""},
		{"null", "null"},
		{"array", `["theme"]`},
		{"scalar", `"dark"`},
		{"malformed", `{"theme":`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodePatch([]byte(tc.body), maxDoc, maxKeys); err == nil {
				t.Fatalf("accepted a non-object body: %q", tc.body)
			}
		})
	}
	if _, err := decodePatch([]byte(`{"theme":"dark"}`), maxDoc, maxKeys); err != nil {
		t.Fatalf("rejected a valid object: %v", err)
	}
}

// An empty object is a VALID no-op patch — a client that computed no changes
// should get a 200 with its document back, not a 400.
func TestDecodePatch_EmptyObjectIsValid(t *testing.T) {
	patch, err := decodePatch([]byte(`{}`), maxDoc, maxKeys)
	if err != nil {
		t.Fatalf("empty object refused: %v", err)
	}
	if len(patch) != 0 {
		t.Fatalf("expected an empty patch, got %v", patch)
	}
}

func TestDecodePatch_BoundsBodyAndKeys(t *testing.T) {
	if _, err := decodePatch([]byte(`{"k":"`+strings.Repeat("x", maxDoc)+`"}`), maxDoc, maxKeys); err == nil {
		t.Fatal("an over-sized body was accepted")
	}
	if _, err := decodePatch([]byte(`{"a":1,"b":2,"c":3}`), maxDoc, 2); err == nil {
		t.Fatal("a patch exceeding the key bound was accepted")
	}
}
