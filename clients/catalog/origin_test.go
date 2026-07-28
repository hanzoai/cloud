package catalog

import (
	"context"
	"errors"
	"testing"

	"github.com/hanzoai/cloud/clients/projects"
	"github.com/hanzoai/cloud/clients/templates"
)

// TestOriginSeparatesTheNouns is the bug this field exists for: a curated
// starter, a stranger's remix and somebody else's paid UI kit were one
// undifferentiated list, so a visitor could not tell what any row WAS. Each lane
// is derived from where the thing lives, and the four must come out distinct.
func TestOriginSeparatesTheNouns(t *testing.T) {
	t.Setenv(platformOrgEnv, "hanzo")
	t.Setenv(sourceOrgsEnv, "-")
	gallery(t, []templates.Template{{Slug: "folio", Title: "Folio"}})
	restore(t, []projects.LiveSite{
		// the curated starter's own demo
		{Org: "hanzo", Slug: "folio", Name: "Folio", URL: "https://folio.hanzo.app"},
		// one of our seeded examples: built ON the platform, badged ours
		{Org: "hanzo", Slug: "ex-kanban", Name: "Kanban", URL: "https://ex-kanban.hanzo.app", Official: true},
		// somebody else's kit we host and credit
		{Org: "hanzo", Slug: "kinetic", Name: "Fitness Pro", URL: "https://kinetic.hanzo.app",
			Upstream: "UI8 — Fitness Pro", License: "UI8 commercial licence"},
	})
	repo := fromRepo(ghRepo{Name: "cloud", HTMLURL: "https://github.com/hanzoai/cloud"}, hz)

	got := map[string]string{}
	for _, e := range publish(t, []Entry{repo}) {
		got[e.ID] = e.Origin
	}
	for id, want := range map[string]string{
		"hanzo/folio":     OriginTemplate,
		"hanzo/ex-kanban": OriginCommunity,
		"hanzo/kinetic":   OriginThirdParty,
		"hanzo/cloud":     OriginProduct,
	} {
		if got[id] != want {
			t.Errorf("%s origin = %q, want %q", id, got[id], want)
		}
	}
}

// TestStartersAreReadFromTheGallery pins the derivation: a demo is one of OUR
// starters because the CURATED GALLERY says that slug is a starter, not because
// the name pattern looks right. The three slugs are the three the fork flow
// derives (clients/projects fork.go), and anything else is somebody's app.
func TestStartersAreReadFromTheGallery(t *testing.T) {
	gallery(t, []templates.Template{
		{Slug: "metrics", Title: "Metrics"}, // reserved name ⇒ deploys as metrics-template
		{Slug: "cipher", Title: "Cipher", Variants: []templates.Variant{
			{ID: "html"}, {ID: "react"},
		}},
	})
	s := starters()
	for _, slug := range []string{"metrics", "metrics-template", "cipher", "cipher-html", "cipher-react"} {
		if !s[slug] {
			t.Errorf("%q is a shape of a curated starter and must be in the templates lane", slug)
		}
	}
	for _, slug := range []string{"cipher-clone", "ex-kanban", "metricsx"} {
		if s[slug] {
			t.Errorf("%q is nobody's starter — spelling must not put an app in the templates lane", slug)
		}
	}
}

// TestLineageOutranksTheGallery: a recorded fork parent is proof somebody BUILT
// this, and it wins over a slug that happens to match a starter. Otherwise a
// remix that kept its parent's name would be filed as our own curated starter.
func TestLineageOutranksTheGallery(t *testing.T) {
	gallery(t, []templates.Template{{Slug: "folio", Title: "Folio"}})
	s := starters()
	remix := projects.LiveSite{Org: "acme", Slug: "folio", ForkedFrom: "folio"}
	if got := siteOrigin(remix, s); got != OriginCommunity {
		t.Errorf("a remix of folio is community, got %q", got)
	}
	if got := siteOrigin(projects.LiveSite{Org: "hanzo", Slug: "folio"}, s); got != OriginTemplate {
		t.Errorf("the starter's own demo is the templates lane, got %q", got)
	}
}

// TestThirdPartyIsCreditedOrNotListed is rule four: a fork holds somebody else's
// code under one of OUR org headers, so it is either attributed or it is not in
// the catalog. There is no third state where it is shown with no author on it.
func TestThirdPartyIsCreditedOrNotListed(t *testing.T) {
	named := ghRepo{Name: "ui", FullName: "hanzo-apps/ui", Fork: true}
	reads(t, func(context.Context, string) (ghRepo, error) {
		r := ghRepo{}
		r.License.SPDX = "MIT"
		r.Parent = &struct {
			FullName string `json:"full_name"`
		}{FullName: "frappe/ui"}
		return r, nil
	})
	if !credit(context.Background(), &named) {
		t.Fatal("a fork whose parent GitHub names must be listed, with the credit")
	}
	e := fromRepo(named, hz)
	if e.Origin != OriginThirdParty || e.Upstream != "frappe/ui" || e.License != "MIT" {
		t.Errorf("a credited fork must carry whose it is: %+v", e)
	}
	if e.Official || e.Forkable {
		t.Errorf("somebody else's work is never ours to badge or hand out: %+v", e)
	}

	// GitHub could not be read, or names no parent: not listed.
	reads(t, func(context.Context, string) (ghRepo, error) { return ghRepo{}, errors.New("boom") })
	if unnamed := (ghRepo{Name: "x", FullName: "hanzoai/x", Fork: true}); credit(context.Background(), &unnamed) {
		t.Error("a fork we cannot credit must be dropped, not shown uncredited")
	}
}

// TestUnnamedLicenceStillCredits: GitHub's NOASSERTION means it could not
// identify the licence, which is not a licence name. The row keeps its upstream
// — the half that stops us implying the work is ours — and states no terms.
func TestUnnamedLicenceStillCredits(t *testing.T) {
	r := ghRepo{Name: "sample", FullName: "hanzoai/sample", Fork: true}
	r.License.SPDX = "NOASSERTION"
	r.Parent = &struct {
		FullName string `json:"full_name"`
	}{FullName: "unity/sample"}
	if e := fromRepo(r, hz); e.Upstream != "unity/sample" || e.License != "" {
		t.Errorf("upstream is required, an unnamed licence is blank: %+v", e)
	}
}

// gallery swaps the curated-catalog seam for one test.
func gallery(t *testing.T, cat []templates.Template) {
	t.Helper()
	prev := curated
	curated = func() ([]templates.Template, error) { return cat, nil }
	t.Cleanup(func() { curated = prev })
}

// reads swaps the single-repo GitHub read for one test.
func reads(t *testing.T, fn func(context.Context, string) (ghRepo, error)) {
	t.Helper()
	prev := repoRead
	repoRead = fn
	t.Cleanup(func() { repoRead = prev })
}
