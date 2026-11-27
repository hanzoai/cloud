package projects

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// repoToken is shaped like a GitHub OAuth token; it only has to be findable.
const repoToken = "gho_0123456789abcdefghijABCDEFGHIJ012345"

const (
	credURL  = "https://x-access-token:" + repoToken + "@github.com/hanzoai/hanzo.sh"
	cleanURL = "https://github.com/hanzoai/hanzo.sh"
)

func noToken(t *testing.T, where string, b []byte) {
	t.Helper()
	if s := string(b); strings.Contains(s, repoToken) || strings.Contains(s, "x-access-token") {
		t.Fatalf("%s carries the clone credential: %s", where, s)
	}
}

func storedRepoURL(t *testing.T, org, slug string) string {
	t.Helper()
	var u string
	if err := mounted.State.store.db.QueryRow(`SELECT repo_url FROM projects WHERE org=$1 AND slug=$2`, org, slug).Scan(&u); err != nil {
		t.Fatalf("read repo_url: %v", err)
	}
	return u
}

// A clone credential sent in repo.url is neither stored nor served: not by the
// create or update response, the detail, the list, or the store row itself.
func TestRepoCredentialNeverStoredOrServed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)

		code, body := do(t, app, http.MethodPost, "/v1/project", "acme",
			map[string]any{"slug": "sh", "name": "sh", "repo": map[string]any{"url": credURL, "branch": "main"}})
		if code != http.StatusCreated {
			t.Fatalf("create want 201, got %d (%s)", code, body)
		}
		noToken(t, "create response", body)
		var p projectsProject
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatalf("create json: %v", err)
		}
		if p.Repo.URL != cleanURL || p.Repo.Provider != "github" {
			t.Fatalf("repo = %+v, want url %q provider github", p.Repo, cleanURL)
		}
		if got := storedRepoURL(t, "acme", "sh"); got != cleanURL {
			t.Fatalf("stored repo_url = %q, want %q", got, cleanURL)
		}

		code, body = do(t, app, http.MethodPatch, "/v1/project/sh", "acme",
			map[string]any{"repo": map[string]any{"url": "https://oauth2:" + repoToken + "@github.com/hanzoai/hanzo.sh", "branch": "main"}})
		if code != http.StatusOK {
			t.Fatalf("update want 200, got %d (%s)", code, body)
		}
		noToken(t, "update response", body)
		if got := storedRepoURL(t, "acme", "sh"); got != cleanURL {
			t.Fatalf("stored repo_url after update = %q, want %q", got, cleanURL)
		}

		for _, path := range []string{"/v1/project/sh", "/v1/project", "/v1/project"} {
			code, body = do(t, app, http.MethodGet, path, "acme", nil)
			if code != http.StatusOK {
				t.Fatalf("GET %s want 200, got %d (%s)", path, code, body)
			}
			noToken(t, "GET "+path, body)
			if !strings.Contains(string(body), cleanURL) {
				t.Fatalf("GET %s lost the repo link: %s", path, body)
			}
		}
	})
}

// A row that already holds a credential is redacted on every read: the detail,
// the list, the cross-org catalogue, and a fork that copies its source.
func TestStoredRepoCredentialIsRedactedOnRead(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		ctx := context.Background()

		p := mkProject("hanzo", "sh", "sh")
		p.Status = "live"
		if err := mounted.State.store.CreateProject(ctx, p); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if _, err := mounted.State.store.db.Exec(`UPDATE projects SET repo_url=$1 WHERE org=$2 AND slug=$3`, credURL, "hanzo", "sh"); err != nil {
			t.Fatalf("plant credential: %v", err)
		}

		for _, path := range []string{"/v1/project/sh", "/v1/project"} {
			code, body := do(t, app, http.MethodGet, path, "hanzo", nil)
			if code != http.StatusOK {
				t.Fatalf("GET %s want 200, got %d (%s)", path, code, body)
			}
			noToken(t, "GET "+path, body)
		}

		sites, err := LiveSites(ctx)
		if err != nil {
			t.Fatalf("LiveSites: %v", err)
		}
		seen := false
		for _, s := range sites {
			if s.Org == "hanzo" && s.Slug == "sh" {
				seen = true
				if s.Repo != cleanURL {
					t.Fatalf("catalogue repo = %q, want %q", s.Repo, cleanURL)
				}
			}
		}
		if !seen {
			t.Fatal("the live public project must reach the catalogue")
		}

		code, body := do(t, app, http.MethodPost, "/v1/project/fork", "acme",
			map[string]any{"slug": "sh", "target": "my-sh"})
		if code != http.StatusCreated {
			t.Fatalf("fork want 201, got %d (%s)", code, body)
		}
		noToken(t, "fork response", body)
		if got := storedRepoURL(t, "acme", "my-sh"); got != cleanURL {
			t.Fatalf("forked repo_url = %q, want %q", got, cleanURL)
		}
	})
}
