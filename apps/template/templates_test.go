package template

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
		if tpl.Source == "" {
			t.Fatalf("template %q has no source", tpl.Slug)
		}
		// A row needs somewhere to send the browser: a screenshot or a live demo.
		// Templates the gallery never screenshotted carry the demo.
		if tpl.Preview == "" && tpl.Demo == "" && !noVisual[tpl.Slug] {
			t.Fatalf("template %q has neither a preview nor a demo", tpl.Slug)
		}
	}
}

// noVisual names the rows that carry no screenshot and no demo, with why. It is a
// list of EXCEPTIONS rather than a softer rule, so adding one is a decision
// somebody writes down and the other 59 rows stay held to the bar.
//
// A screenshot was dropped from all four by 7814ddb804, which repointed previews
// from gallery.hanzo.ai to hanzo.app — correctly, since the old host now 404s
// everything. These four had no image on the new host either, so the commit
// removed a dead link rather than carrying one.
//
// Measured, with controls: circle and kinetic resolve 200 at
// hanzo.app/templates/<slug>.webp, and all four of these 404 there AND at the
// retired gallery host.
var noVisual = map[string]bool{
	// Native iOS. There is no web artifact to screenshot or deploy, and the
	// source is public (github.com/hanzo-templates/*, verified 200), so the repo
	// IS where a browser goes. A demo URL here would be a fiction.
	"swiftui":      true,
	"swiftui-chat": true,

	// Web templates whose screenshot was never migrated. These two DO owe one —
	// their source is private, so today a visitor has nowhere to land at all.
	// Remove the entry when the image lands; do not add a demo URL that 404s.
	"quantum":   true,
	"jobfinder": true,
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
