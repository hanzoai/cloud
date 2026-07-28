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
		// A row needs somewhere to send the browser: a screenshot, a live demo,
		// or both. Templates the gallery never screenshotted carry the demo.
		if tpl.Source == "" || (tpl.Preview == "" && tpl.Demo == "") {
			t.Fatalf("template %q missing source and preview/demo handoff URL", tpl.Slug)
		}
	}
}

// TestVariantsAreOptionsNotSiblings is the anti-regression for the defect this
// catalog was rebuilt to fix: format/page/theme used to be spent as sibling
// slugs, so one portfolio template occupied 26 rows and two of its "templates"
// deployed byte-identical demos. A variant must be reachable ONLY through its
// template, never as a catalog row of its own.
func TestVariantsAreOptionsNotSiblings(t *testing.T) {
	cat, err := catalog()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	slugs := map[string]bool{}
	for _, tpl := range cat {
		slugs[tpl.Slug] = true
	}
	for _, tpl := range cat {
		ids := map[string]bool{}
		for _, v := range tpl.Variants {
			if v.ID == "" || v.Label == "" || v.Source == "" {
				t.Fatalf("template %q variant %+v missing id/label/source", tpl.Slug, v)
			}
			if ids[v.ID] {
				t.Fatalf("template %q has duplicate variant id %q", tpl.Slug, v.ID)
			}
			ids[v.ID] = true
			switch v.Kind {
			case "format", "page", "theme":
			default:
				t.Fatalf("template %q variant %q kind %q is not format/page/theme", tpl.Slug, v.ID, v.Kind)
			}
			if slugs[tpl.Slug+"-"+v.ID] {
				t.Fatalf("variant %q of %q is ALSO a catalog slug — one template, one entry",
					v.ID, tpl.Slug)
			}
			// Every variant resolves, and resolution fills the framework in.
			got, ok := tpl.Variant(v.ID)
			if !ok || got.Framework == "" {
				t.Fatalf("template %q variant %q did not resolve: %+v (ok=%v)", tpl.Slug, v.ID, got, ok)
			}
		}
		// No preference resolves to a usable shape for EVERY template, with or
		// without variants; an unknown id never does.
		if got, ok := tpl.Variant(""); !ok || got.Source == "" || got.Framework == "" {
			t.Fatalf("template %q default variant did not resolve: %+v (ok=%v)", tpl.Slug, got, ok)
		}
		if _, ok := tpl.Variant("no-such-variant"); ok {
			t.Fatalf("template %q resolved an unknown variant", tpl.Slug)
		}
	}
}
