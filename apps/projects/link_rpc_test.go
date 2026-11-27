package projects

// link_rpc_test.go — a linked repository, through the generated client, over a
// real socket, into the real store, and back out of GET /v1/project.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/client"
	projectpeer "github.com/hanzoai/cloud/client/project"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

const widgets = "https://github.com/acme-gh/widgets.git"

// listed is the caller's GET /v1/project.
func listed(t *testing.T, app *zip.App, org string) []projectsProject {
	t.Helper()
	code, body := do(t, app, http.MethodGet, "/v1/project", org, nil)
	if code != http.StatusOK {
		t.Fatalf("list as %s: %d (%s)", org, code, body)
	}
	var out []projectsProject
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("list json: %v (%s)", err, body)
	}
	return out
}

func link(t *testing.T, org, repo string) *client.ProjectOut {
	t.Helper()
	out, err := projectpeer.ProjectsLink(stated(org), &client.LinkIn{Repo: repo})
	if err != nil {
		t.Fatalf("link %s as %s: %v", repo, org, err)
	}
	return out
}

// A LINKED REPOSITORY IS A PROJECT: GET /v1/project lists it, named for the
// repository and building from it, its own read answers it, and no other org
// sees either.
func TestALinkedRepositoryIsAProject(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := servedCreate(t)
		out := link(t, "acme", widgets)
		if out.Slug != "acme-gh-widgets" {
			t.Fatalf("slug = %q, want acme-gh-widgets", out.Slug)
		}
		rows := listed(t, app, "acme")
		if len(rows) != 1 {
			t.Fatalf("acme lists %d projects, want 1: %+v", len(rows), rows)
		}
		p := rows[0]
		if p.ID != out.ID || p.Name != "acme-gh/widgets" || p.Repo.URL != widgets || p.Repo.Provider != "github" ||
			p.Repo.Branch != "main" || p.Status != "draft" || p.LiveURL != "" || p.Visibility != Public {
			t.Fatalf("listed %+v", p)
		}
		if code, body := do(t, app, http.MethodGet, "/v1/project/"+out.Slug, "acme", nil); code != http.StatusOK {
			t.Fatalf("its own read: %d (%s)", code, body)
		}
		if rows := listed(t, app, "other"); len(rows) != 0 {
			t.Fatalf("another org lists %+v", rows)
		}
		if code, _ := do(t, app, http.MethodGet, "/v1/project/"+out.Slug, "other", nil); code != http.StatusNotFound {
			t.Fatalf("another org reads it: %d", code)
		}
	})
}

// ONE PROJECT PER REPOSITORY: linking again, under another spelling of the same
// address, answers the same project; so does a project someone made by hand
// from that repository.
func TestALinkIsOneProjectPerRepository(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := servedCreate(t)
		first := link(t, "acme", widgets)
		for _, again := range []string{
			"https://github.com/acme-gh/widgets",
			"https://github.com/Acme-GH/Widgets.git",
			"https://x-access-token:t@github.com/acme-gh/widgets.git",
		} {
			if out := link(t, "acme", again); out.ID != first.ID {
				t.Fatalf("%s made %+v, want %+v", again, out, first)
			}
		}
		if code, body := do(t, app, http.MethodPost, "/v1/project", "acme",
			map[string]any{"name": "Shop Front", "repo": map[string]any{"url": "https://github.com/acme-gh/shop"}}); code != http.StatusCreated {
			t.Fatalf("seed: %d (%s)", code, body)
		}
		if out := link(t, "acme", "https://github.com/acme-gh/shop.git"); out.Slug != "shop-front" {
			t.Fatalf("a hand-made project of the repository was not the answer: %+v", out)
		}
		if rows := listed(t, app, "acme"); len(rows) != 2 {
			t.Fatalf("acme lists %d projects, want 2: %+v", len(rows), rows)
		}
		// The same repository in another org is that org's own project.
		if out := link(t, "other", widgets); out.ID == first.ID {
			t.Fatal("two orgs share one project")
		}
	})
}

// A TAKEN NAME IS NUMBERED, never refused: the link is the org's, not a choice
// of slug, so it takes the next one free.
func TestALinkTakesTheNextFreeSlug(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := servedCreate(t)
		if code, body := do(t, app, http.MethodPost, "/v1/project", "acme",
			map[string]any{"name": "Widgets", "slug": "acme-gh-widgets"}); code != http.StatusCreated {
			t.Fatalf("seed: %d (%s)", code, body)
		}
		if out := link(t, "acme", widgets); out.Slug != "acme-gh-widgets-2" {
			t.Fatalf("slug = %q, want acme-gh-widgets-2", out.Slug)
		}
	})
}

// A LINK PUBLISHES NOTHING: the repository is the project's source, so no
// community copy is queued for it.
func TestALinkPublishesNoSource(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		servedCreate(t)
		link(t, "acme", widgets)
		q := mounted.State.queue
		q.mu.Lock()
		defer q.mu.Unlock()
		if len(q.work) != 0 {
			t.Fatalf("a link queued a community copy: %v", q.work)
		}
	})
}

// NO CALLER, NO PROJECT; and an address that names no repository is refused.
func TestALinkNeedsACallerAndARepository(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		servedCreate(t)
		if _, err := projectpeer.ProjectsLink(context.Background(), &client.LinkIn{Repo: widgets}); err == nil {
			t.Fatal("a project was linked for nobody")
		}
		for _, bad := range []string{"", "git@github.com:acme-gh/widgets.git", "https://github.com/acme-gh", "http://github.com/acme-gh/widgets"} {
			_, err := projectpeer.ProjectsLink(stated("acme"), &client.LinkIn{Repo: bad})
			if he, ok := errors.AsType[*zip.HTTPError](err); !ok || he.Status != http.StatusBadRequest {
				t.Errorf("%q answered %v, want a 400", bad, err)
			}
		}
	})
}
