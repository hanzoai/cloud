package sites

import (
	"testing"

	"github.com/hanzoai/cloud/apps/template"
)

// TestEveryTemplateDemoIsItsOwnHost ties the public gallery to the site host rules,
// which only this app knows. A demo is the template's own <slug>.hanzo.app or it is
// absent: a gallery row must never advertise another template's deploy, so browsing
// one shows its own product.
//
// The one derived exception is the one a fork already derives: a slug that is a
// reserved subdomain (IsReserved) cannot BE a host, so its deploy carries the same
// `-template` suffix a fork appends. Reading the predicate instead of listing labels
// keeps the two in lock-step.
func TestEveryTemplateDemoIsItsOwnHost(t *testing.T) {
	cat, err := template.List()
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
		} else if IsReserved(host) {
			host += "-template"
		}
		if want := "https://" + host + ".hanzo.app"; tpl.Demo != want {
			t.Errorf("template %q demo is %q, not its own deploy %q", tpl.Slug, tpl.Demo, want)
		}
	}
}
