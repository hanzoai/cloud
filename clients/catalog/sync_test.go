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
		{Org: "hanzo", Slug: "gallery", Name: "Gallery", URL: "https://gallery.hanzo.app"},
		{Org: "acme", Slug: "crm", Name: "CRM", URL: "https://crm.hanzo.app"},
	})

	pub, byOrg, _ := corpus(context.Background())
	if len(pub) != 1 || pub[0].ID != "hanzo/gallery" {
		t.Fatalf("only the platform's own site is published: %+v", pub)
	}
	if !pub[0].Forkable {
		t.Error("a published demo is meant to be forkable")
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

// restore swaps the live-sites seam for the duration of one test.
func restore(t *testing.T, sites []projects.LiveSite) {
	t.Helper()
	prev := liveSites
	liveSites = func(context.Context) ([]projects.LiveSite, error) { return sites, nil }
	t.Cleanup(func() { liveSites = prev })
}
