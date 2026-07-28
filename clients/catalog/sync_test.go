package catalog

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud/clients/projects"
)

// TestSyncRoutesSitesByOrg is the tenancy rule the sync itself must keep: our own
// org's live sites are published to the world, every other org's land in that
// org's own corpus and nowhere else. A regression here is a customer's project on
// a public page, so it is asserted on the ROUTING, not on the query that reads it.
func TestSyncRoutesSitesByOrg(t *testing.T) {
	t.Setenv(platformOrgEnv, "hanzo")
	t.Setenv(sourceOrgsEnv, "-") // no GitHub in this test; a bogus org fails its fetch
	restore(t, []projects.LiveSite{
		{Org: "hanzo", Slug: "gallery", Name: "Gallery", URL: "https://gallery.hanzo.app",
			Repo: "https://github.com/hanzo-apps/gallery"},
		{Org: "acme", Slug: "crm", Name: "CRM", URL: "https://crm.hanzo.app"},
	})

	pub, byOrg, _ := corpus(context.Background())
	if len(pub) != 1 || pub[0].ID != "hanzo/gallery" {
		t.Fatalf("only the platform's own site is published: %+v", pub)
	}
	if !pub[0].Forkable || pub[0].Repo != "https://github.com/hanzo-apps/gallery" {
		t.Errorf("a demo with a declared source is forkable AND names it: %+v", pub[0])
	}
	if got := byOrg["acme"]; len(got) != 1 || got[0].ID != "acme/crm" {
		t.Fatalf("acme's site must land in acme's corpus: %+v", byOrg)
	}
	if _, leaked := byOrg[PublicOrg]; leaked {
		t.Fatal("a tenant corpus was written under the published org")
	}
}

// TestArchetypeIsDerivedNotGuessed pins the browse axis: it comes from the repo's
// own words, and a word that only APPEARS inside another word does not count.
func TestArchetypeIsDerivedNotGuessed(t *testing.T) {
	for _, tc := range []struct {
		repo ghRepo
		want string
	}{
		{ghRepo{Name: "node", Description: "the blockchain node"}, "chain"},
		{ghRepo{Name: "python-sdk", Description: "client library"}, "sdk"},
		{ghRepo{Name: "nextjs-template", IsTemplate: true}, "template"},
		{ghRepo{Name: "operator", Topics: []string{"kubernetes"}}, "infra"},
		{ghRepo{Name: "zen-omni", Description: "an LLM"}, "model"},
		{ghRepo{Name: "vmware-tools", Description: "unrelated"}, ""}, // "vm" must not fire
		{ghRepo{Name: "misc", Description: "no signal at all"}, ""},
	} {
		if got := archetype(tc.repo); got != tc.want {
			t.Errorf("archetype(%q/%q) = %q, want %q", tc.repo.Name, tc.repo.Description, got, tc.want)
		}
	}
}

// TestRepoMapsToBrand proves a row is filed under the BRAND a person browses by,
// not the GitHub org it happens to live in.
func TestRepoMapsToBrand(t *testing.T) {
	e := fromRepo(ghRepo{
		Name: "node", Description: "lux node", Language: "Go", Stars: 12,
		HTMLURL: "https://github.com/luxfi/node", Homepage: "lux.network", PushedAt: "2026-07-02T00:00:00Z",
	}, "lux")
	if e.ID != "lux/node" || e.Org != "lux" || e.Repo != "https://github.com/luxfi/node" {
		t.Fatalf("bad mapping: %+v", e)
	}
	if e.URL != "https://lux.network" {
		t.Errorf("a scheme-less homepage must be made a real link, got %q", e.URL)
	}
	if !e.Forkable {
		t.Error("a public repo is forkable")
	}
}

// TestSourceOrgsOverride pins the "githuborg:brand" form the env takes.
func TestSourceOrgsOverride(t *testing.T) {
	t.Setenv(sourceOrgsEnv, "luxfi:lux, zooai:zoo ,solo")
	got := sourceOrgs()
	for gh, brand := range map[string]string{"luxfi": "lux", "zooai": "zoo", "solo": "solo"} {
		if got[gh] != brand {
			t.Errorf("sourceOrgs()[%q] = %q, want %q", gh, got[gh], brand)
		}
	}
	if len(got) != 3 {
		t.Errorf("override must replace the default set, got %v", got)
	}
}

// TestSiteCarriesItsSource is gap (b): a live demo must name the repo it came
// from, or the catalog is a wall of screenshots. It also pins the regression that
// caused it — a site and its repo key on the SAME "<org>/<name>", so the site row
// used to overwrite the repo row and delete the only link back to the source.
func TestSiteCarriesItsSource(t *testing.T) {
	t.Setenv(platformOrgEnv, "hanzo")
	t.Setenv(sourceOrgsEnv, "-")
	restore(t, []projects.LiveSite{
		// declares nothing; the repo of the same name is the only source there is
		{Org: "hanzo", Slug: "kart-racer", Name: "Kart Racer", URL: "https://kart-racer.hanzo.app"},
		// declares its own source, which must outrank any name match
		{Org: "hanzo", Slug: "kanban", Name: "Kanban", URL: "https://kanban.hanzo.app",
			Repo: "https://github.com/hanzo-apps/kanban-lane", ForkedFrom: "hanzo/example-kanban"},
	})
	repo := fromRepo(ghRepo{
		Name: "kart-racer", Description: "a kart game", Language: "TypeScript", Stars: 7,
		HTMLURL: "https://github.com/hanzo-templates/kart-racer", PushedAt: "2026-01-01T00:00:00Z",
	}, "hanzo")

	got := map[string]Entry{}
	for _, e := range publish(t, []Entry{repo}) {
		got[e.ID] = e
	}
	if len(got) != 2 {
		t.Fatalf("a demo and its repo are ONE thing, want 2 rows, got %d: %+v", len(got), got)
	}
	kart := got["hanzo/kart-racer"]
	if kart.Repo != "https://github.com/hanzo-templates/kart-racer" {
		t.Errorf("the demo lost its source: %+v", kart)
	}
	if kart.URL != "https://kart-racer.hanzo.app" || kart.Kind != "site" {
		t.Errorf("the live facts must win: %+v", kart)
	}
	if kart.Description != "a kart game" || kart.Language != "TypeScript" || kart.Stars != 7 {
		t.Errorf("the repo's facts must survive the fold: %+v", kart)
	}
	if kart.Title != "Kart Racer" {
		t.Errorf("the project's human title must win: %q", kart.Title)
	}
	kanban := got["hanzo/kanban"]
	if kanban.Repo != "https://github.com/hanzo-apps/kanban-lane" {
		t.Errorf("a declared source is authoritative: %+v", kanban)
	}
	if kanban.Template != "hanzo/example-kanban" {
		t.Errorf("lineage must reach the catalog so a fork can credit its parent: %+v", kanban)
	}
}

// TestForkableDiscriminates is gap (a): the axis has to be able to say NO, or the
// pill filters nothing. The three honest noes are a demo with no source to hand
// over, a repo that is someone else's to hand over, and a demo of one.
func TestForkableDiscriminates(t *testing.T) {
	t.Setenv(platformOrgEnv, "hanzo")
	t.Setenv(sourceOrgsEnv, "-")
	restore(t, []projects.LiveSite{
		{Org: "hanzo", Slug: "ex-askdocs", Name: "Ask Docs", URL: "https://ex-askdocs.hanzo.app"},
		{Org: "hanzo", Slug: "engine", Name: "Engine", URL: "https://engine.hanzo.app"},
	})
	ours := fromRepo(ghRepo{Name: "console", HTMLURL: "https://github.com/hanzoai/console"}, "hanzo")
	theirs := fromRepo(ghRepo{Name: "engine", HTMLURL: "https://github.com/hanzoai/engine", Fork: true}, "hanzo")

	want := map[string]bool{
		"hanzo/console":    true,  // ours, public, nobody else's lineage
		"hanzo/engine":     false, // a fork of a third-party upstream — theirs, not ours
		"hanzo/ex-askdocs": false, // live, but there is no source to hand anyone
	}
	for _, e := range publish(t, []Entry{ours, theirs}) {
		if w, ok := want[e.ID]; ok && e.Forkable != w {
			t.Errorf("%s forkable = %v, want %v (repo %q)", e.ID, e.Forkable, w, e.Repo)
		}
		delete(want, e.ID)
	}
	if len(want) != 0 {
		t.Fatalf("rows never published: %v", want)
	}
}

// publish runs the reconcile with a fixed repo set standing in for GitHub, and
// returns the published corpus.
func publish(t *testing.T, repos []Entry) []Entry {
	t.Helper()
	prev := fromOrgs
	fromOrgs = func(context.Context) ([]Entry, error) { return repos, nil }
	t.Cleanup(func() { fromOrgs = prev })
	pub, _, _ := corpus(context.Background())
	return pub
}

// restore swaps the live-sites seam for the duration of one test.
func restore(t *testing.T, sites []projects.LiveSite) {
	t.Helper()
	prev := liveSites
	liveSites = func(context.Context) ([]projects.LiveSite, error) { return sites, nil }
	t.Cleanup(func() { liveSites = prev })
}
