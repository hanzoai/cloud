package templates

import (
	"strings"
	"testing"

	"github.com/hanzoai/cloud/clients/sites"
)

// catalog_test.go pins what makes a catalog row WORTH shipping, as opposed to
// merely well-formed (templates_test.go already covers the shape). Every rule
// here failed on live data before it was written, so each one is an
// anti-regression, not an aspiration.

// TestEveryTemplateDescribesItself: 43 of 66 rows shipped `"description": ""`.
// The description is not decoration — clients/projects/fork.go copies it onto the
// forked project, so an empty one propagates into every customer's project list.
func TestEveryTemplateDescribesItself(t *testing.T) {
	cat, err := catalog()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	for _, tpl := range cat {
		d := strings.TrimSpace(tpl.Description)
		switch {
		case d == "":
			t.Errorf("template %q has no description", tpl.Slug)
		case len(d) < 24:
			t.Errorf("template %q description is not a sentence: %q", tpl.Slug, d)
		case strings.Contains(d, "\n"):
			t.Errorf("template %q description is not ONE line", tpl.Slug)
		}
	}
}

// TestSourceIsARepository: every gallery row pointed `source` at
// https://gallery.hanzo.ai/templates/<slug>, which 404s — and fork.go assigns
// that string to createReq.Repo.URL, so forking handed the builder an HTML error
// page as a git remote. Source names the REPOSITORY or it names nothing usable.
func TestSourceIsARepository(t *testing.T) {
	cat, err := catalog()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	for _, tpl := range cat {
		for _, src := range append([]string{tpl.Source}, variantSources(tpl)...) {
			if !strings.HasPrefix(src, "https://github.com/") {
				t.Errorf("template %q source %q is not a repository URL", tpl.Slug, src)
			}
		}
	}
}

// TestDemoIsTheTemplatesOwnHost is the rule that ends the crossing class of bug:
// seven rows advertised a demo that was some OTHER template's deploy (Blocks
// pointed at forge.hanzo.app, which renders "Streamline"; Loop pointed at
// blocks.hanzo.app, which renders Bento v.3; Canvas at studio.hanzo.app, …), so
// browsing a template showed a stranger's product. A demo is the template's own
// <slug>.hanzo.app or it is absent.
//
// The one derived exception is the one fork.go already derives: a slug that is a
// reserved subdomain (clients/sites/reserved.go — `metrics`) cannot BE a host, so
// its deploy carries the same `-template` suffix fork.go appends. Reading the
// predicate instead of listing the labels keeps the two in lock-step.
func TestDemoIsTheTemplatesOwnHost(t *testing.T) {
	cat, err := catalog()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	// innovise ships from the repo (and therefore the host) `catalyst`, whose page
	// is titled "Innovise" — the name on the page is the corroboration.
	elsewhere := map[string]string{"innovise": "catalyst"}
	for _, tpl := range cat {
		if tpl.Demo == "" {
			continue // a template with no live deploy says so; that is not a defect
		}
		host := tpl.Slug
		if h, ok := elsewhere[tpl.Slug]; ok {
			host = h
		} else if sites.IsReserved(host) {
			host += "-template"
		}
		if want := "https://" + host + ".hanzo.app"; tpl.Demo != want {
			t.Errorf("template %q demo is %q, not its own deploy %q", tpl.Slug, tpl.Demo, want)
		}
	}
}

// TestSlugIsASingleCleanName: a slug is the name a person types and the name the
// forked project gets. Doubled compounds (`helpdesk-deskline`) say the same thing
// twice, and a slug that is a reserved subdomain can never be published as a
// site at all.
func TestSlugIsASingleCleanName(t *testing.T) {
	cat, err := catalog()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	for _, tpl := range cat {
		if !slugRE.MatchString(tpl.Slug) {
			t.Errorf("template slug %q is not a DNS label", tpl.Slug)
		}
		for _, w := range strings.Split(tpl.Slug, "-") {
			if strings.Count(tpl.Slug, w) > 1 {
				t.Errorf("template slug %q repeats %q", tpl.Slug, w)
			}
		}
	}
}

func variantSources(tpl Template) []string {
	out := make([]string, 0, len(tpl.Variants))
	for _, v := range tpl.Variants {
		out = append(out, v.Source)
	}
	return out
}
