package provider

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	projectpeer "github.com/hanzoai/cloud/client/project"
	"github.com/hanzoai/cloud/sdk"
)

// One Hanzo org may hold several GitHub installations, so a repository name is
// unique only within its owner. These are the three that share the name today.
func grantedFixture() []githubRepo {
	return []githubRepo{
		{Name: "ai", FullName: "hanzoai/ai", CloneURL: "https://github.com/hanzoai/ai.git"},
		{Name: "ai", FullName: "hanzo-apps/ai", CloneURL: "https://github.com/hanzo-apps/ai.git"},
		{Name: "ai", FullName: "hanzo-docs/ai", CloneURL: "https://github.com/hanzo-docs/ai.git"},
		{Name: "cloud", FullName: "hanzoai/cloud", CloneURL: "https://github.com/hanzoai/cloud.git"},
		{Name: "old", FullName: "hanzoai/old", Archived: true},
	}
}

func TestBareNameAcrossOwnersIsRefused(t *testing.T) {
	_, err := selectImports(grantedFixture(), []string{"ai"}, false)
	if err == nil {
		t.Fatal("a bare name matching three owners must not import one of them")
	}
	for _, want := range []string{"hanzoai/ai", "hanzo-apps/ai", "hanzo-docs/ai"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %s so the caller can choose; got %v", want, err)
		}
	}
}

func TestQualifiedNameSelectsThatOwner(t *testing.T) {
	items, err := selectImports(grantedFixture(), []string{"hanzo-apps/ai"}, false)
	if err != nil {
		t.Fatalf("qualified selector: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("want 1 repo, got %d", len(items))
	}
	if items[0].CloneURL != "https://github.com/hanzo-apps/ai.git" {
		t.Errorf("selected the wrong owner: %s", items[0].CloneURL)
	}
}

// The bug this fixes: {"repos":["ai"]} imported hanzoai/ai when hanzo-apps/ai was
// meant, because a bare name matched the first listing entry.
func TestQualifiedSiblingsAreIndependent(t *testing.T) {
	for _, full := range []string{"hanzoai/ai", "hanzo-apps/ai", "hanzo-docs/ai"} {
		items, err := selectImports(grantedFixture(), []string{full}, false)
		if err != nil {
			t.Fatalf("%s: %v", full, err)
		}
		if len(items) != 1 || items[0].CloneURL != "https://github.com/"+full+".git" {
			t.Errorf("%s selected %v", full, items)
		}
	}
}

func TestBareNameUniqueToOneOwnerStillWorks(t *testing.T) {
	items, err := selectImports(grantedFixture(), []string{"cloud"}, false)
	if err != nil {
		t.Fatalf("unambiguous bare name: %v", err)
	}
	if len(items) != 1 || items[0].Name != "cloud" {
		t.Errorf("want hanzoai/cloud, got %v", items)
	}
}

func TestDotGitSuffixIsStripped(t *testing.T) {
	if _, err := selectImports(grantedFixture(), []string{"hanzo-apps/ai.git"}, false); err != nil {
		t.Fatalf("owner/name.git should resolve: %v", err)
	}
	if _, err := selectImports(grantedFixture(), []string{"cloud.git"}, false); err != nil {
		t.Fatalf("name.git should resolve: %v", err)
	}
}

// all:true takes the whole granted set, so no selector can be ambiguous.
func TestAllTakesEveryFetchableRepo(t *testing.T) {
	items, err := selectImports(grantedFixture(), nil, true)
	if err != nil {
		t.Fatalf("all: %v", err)
	}
	if len(items) != 4 { // the archived one is unreachable
		t.Fatalf("want 4 fetchable repos, got %d", len(items))
	}
	for _, it := range items {
		if it.Name == "old" {
			t.Error("an archived repo cannot be fetched and must not be queued")
		}
	}
}

func TestArchivedIsNeverSelected(t *testing.T) {
	if _, err := selectImports(grantedFixture(), []string{"hanzoai/old"}, false); err == nil {
		t.Fatal("naming an archived repo must not queue an import that cannot run")
	}
}

func TestNoMatchIsRefused(t *testing.T) {
	if _, err := selectImports(grantedFixture(), []string{"hanzoai/absent"}, false); err == nil {
		t.Fatal("a selector matching nothing must be an error, not an empty success")
	}
}

// EVERY REPOSITORY AN IMPORT NAMES IS ONE OF THE ORG'S PROJECTS: the projects app
// is asked for each, as the importing org and no other.
func TestAnImportAdoptsEachRepositoryAsAProject(t *testing.T) {
	socks(t)
	var (
		mu  sync.Mutex
		got []string
	)
	up(t, projectpeer.App, func(app *zip.App) {
		app.Post("/projects/link", func(ctx context.Context, in *client.LinkIn) (*client.ProjectOut, error) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, cloud.Who(ctx).Org+" "+in.Repo)
			return &client.ProjectOut{ID: "proj_1", Slug: "acme-gh-widgets"}, nil
		}, zip.WithOperationID(client.ProjectsLink))
	})
	adopt(context.Background(), "acme", []githubImportItem{
		{Name: "widgets", Owner: "acme-gh", CloneURL: "https://github.com/acme-gh/widgets.git"},
		{Name: "site", Owner: "acme-gh", CloneURL: "https://github.com/acme-gh/site.git"},
	})
	want := []string{"acme https://github.com/acme-gh/widgets.git", "acme https://github.com/acme-gh/site.git"}
	if !slices.Equal(got, want) {
		t.Fatalf("the projects app was asked %q, want %q", got, want)
	}
}
