package templates

import "testing"

// TestCatalog proves the embedded gallery decodes, is non-empty, and every
// entry a browse row would render has the fields the UI depends on.
func TestCatalog(t *testing.T) {
	cat, err := catalog()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	if len(cat) == 0 {
		t.Fatal("embedded templates catalog is empty")
	}
	seen := map[string]bool{}
	for _, tpl := range cat {
		if tpl.Slug == "" || tpl.Title == "" {
			t.Fatalf("template with empty slug/title: %+v", tpl)
		}
		if seen[tpl.Slug] {
			t.Fatalf("duplicate template slug %q", tpl.Slug)
		}
		seen[tpl.Slug] = true
		if tpl.Source == "" || tpl.Preview == "" {
			t.Fatalf("template %q missing source/preview handoff URL", tpl.Slug)
		}
	}
}
