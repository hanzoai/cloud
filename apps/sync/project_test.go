package sync

// project_test.go — a linked repository is a project: the sync app declares the
// link, the REAL projects app makes the row over a real plane socket, and
// GET /v1/project lists it.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/projects"
	projectpeer "github.com/hanzoai/cloud/client/project"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
)

// world mounts sync and projects side by side, as two apps of one deployment,
// and serves the projects plane on its socket. It answers the sync app and the
// projects app.
func world(t *testing.T) (*zip.App, *zip.App) {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	plane.Bind()
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	sy := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(sy)
	if err := Use(sy, cloud.Deps{}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown() })
	pr := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(pr)
	if err := projects.Use(pr, cloud.Deps{MasterKey: bytes.Repeat([]byte{0x5a}, 32)}); err != nil {
		t.Fatalf("projects: %v", err)
	}
	t.Cleanup(func() { _ = projects.Shutdown() })
	stop, err := cloud.ServePlane(projectpeer.App, nil)
	if err != nil {
		t.Fatalf("serve plane: %v", err)
	}
	t.Cleanup(func() { _ = stop() })
	return sy, pr
}

// listed is org's GET /v1/project: slug → repository.
func listed(t *testing.T, pr *zip.App, org string) map[string]string {
	t.Helper()
	code, body := do(t, pr, http.MethodGet, "/v1/project", org, nil)
	if code != http.StatusOK {
		t.Fatalf("list as %s: %d (%s)", org, code, body)
	}
	var rows []struct {
		Slug string `json:"slug"`
		Repo struct {
			URL string `json:"url"`
		} `json:"repo"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("list json: %v (%s)", err, body)
	}
	out := map[string]string{}
	for _, r := range rows {
		out[r.Slug] = r.Repo.URL
	}
	return out
}

// linkRepo declares org's link to repo and answers its id.
func linkRepo(t *testing.T, sy *zip.App, org, repo string) string {
	t.Helper()
	code, body := do(t, sy, http.MethodPost, "/v1/sync", org, map[string]any{
		"source": map[string]any{"provider": "github", "locator": repo},
	})
	if code != http.StatusOK {
		t.Fatalf("link %s as %s: %d (%s)", repo, org, code, body)
	}
	var v syncView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("link json: %v (%s)", err, body)
	}
	return v.ID
}

// A LINKED REPOSITORY IS LISTED AS A PROJECT, in its own org only, once however
// often it is linked.
func TestALinkedRepositoryIsListedAsAProject(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sy, pr := world(t)
		if got := listed(t, pr, "acme"); len(got) != 0 {
			t.Fatalf("acme starts with %v", got)
		}
		linkRepo(t, sy, "acme", widgetsURL)
		want := map[string]string{"acme-gh-widgets": widgetsURL}
		if got := listed(t, pr, "acme"); len(got) != 1 || got["acme-gh-widgets"] != widgetsURL {
			t.Fatalf("acme lists %v, want %v", got, want)
		}
		if got := listed(t, pr, "beta"); len(got) != 0 {
			t.Fatalf("another org sees acme's link: %v", got)
		}
		linkRepo(t, sy, "acme", widgetsURL)
		if got := listed(t, pr, "acme"); len(got) != 1 {
			t.Fatalf("linking again listed %v", got)
		}
		// The same repository linked by another org is that org's own project. Its
		// slug is a site's address, which no two projects share, so beta's is its
		// own; what it links is the same repository.
		linkRepo(t, sy, "beta", widgetsURL)
		got := listed(t, pr, "beta")
		if len(got) != 1 {
			t.Fatalf("beta lists %v, want one project", got)
		}
		for slug, url := range got {
			if url != widgetsURL || slug == "acme-gh-widgets" {
				t.Fatalf("beta lists %s -> %s, want its own slug for %s", slug, url, widgetsURL)
			}
		}
		if got := listed(t, pr, "acme"); len(got) != 1 {
			t.Fatalf("beta's link reached acme: %v", got)
		}
	})
}

// EVERY REPOSITORY AN ACCOUNT LINK DECLARES IS A PROJECT.
func TestAnAccountLinksEachRepositoryAsAProject(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, pr := world(t)
		acct := Sync{ID: "sync_acct", Org: "webby-ai", Kind: "git", Direction: dirPull, Trigger: trigPoll,
			Source: Endpoint{Provider: provGitHub, Locator: "https://github.com/webby-ai"},
			Target: Endpoint{Provider: provNative, Locator: accountTarget}}
		for _, repo := range []string{"intel-hub", "site"} {
			if _, err := expand(context.Background(), acct, Event{Provider: provGitHub, Repo: repo}); err != nil {
				t.Fatalf("expand %s: %v", repo, err)
			}
		}
		got := listed(t, pr, "webby-ai")
		if len(got) != 2 || got["webby-ai-intel-hub"] != "https://github.com/webby-ai/intel-hub.git" ||
			got["webby-ai-site"] != "https://github.com/webby-ai/site.git" {
			t.Fatalf("webby-ai lists %v", got)
		}
	})
}

// UNLINKING LEAVES THE PROJECT, as it leaves the forge's copy; and a project
// its org deleted is not made again while the link lives.
func TestUnlinkLeavesTheProjectAndADeleteSticks(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		sy, pr := world(t)
		id := linkRepo(t, sy, "acme", widgetsURL)
		if code, body := do(t, sy, http.MethodDelete, "/v1/sync/"+id, "acme", nil); code != http.StatusNoContent {
			t.Fatalf("unlink: %d (%s)", code, body)
		}
		if got := listed(t, pr, "acme"); got["acme-gh-widgets"] != widgetsURL {
			t.Fatalf("unlinking took the project: %v", got)
		}

		gadgets := "https://github.com/acme-gh/gadgets.git"
		linkRepo(t, sy, "acme", gadgets)
		if code, body := do(t, pr, http.MethodDelete, "/v1/project/acme-gh-gadgets", "acme", nil); code != http.StatusNoContent {
			t.Fatalf("delete project: %d (%s)", code, body)
		}
		if !pass(mounted.Load(), context.Background()) {
			t.Fatal("backfill pass did not finish")
		}
		if got := listed(t, pr, "acme"); got["acme-gh-gadgets"] != "" {
			t.Fatalf("a deleted project came back: %v", got)
		}
	})
}

// LINKS THAT PREDATE THIS, OR WHOSE MAKING FAILED, ARE MADE AT BACKFILL.
func TestBackfillMakesEveryLinkAProject(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		_, pr := world(t)
		st, err := storeFor(mounted.Load(), "acme")
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().Unix()
		for _, l := range []Sync{
			{ID: "sync_a", Org: "acme", Kind: "git", Direction: dirPull, Trigger: trigWebhook, CreatedAt: now, UpdatedAt: now,
				Source: Endpoint{Provider: provGitHub, Locator: widgetsURL}, Target: Endpoint{Provider: provNative, Locator: "widgets"}},
			{ID: "sync_acct", Org: "acme", Kind: "git", Direction: dirPull, Trigger: trigPoll, CreatedAt: now, UpdatedAt: now,
				Source: Endpoint{Provider: provGitHub, Locator: "https://github.com/acme-gh"}, Target: Endpoint{Provider: provNative, Locator: accountTarget}},
		} {
			if err := st.Upsert(context.Background(), l); err != nil {
				t.Fatal(err)
			}
		}
		if got := listed(t, pr, "acme"); len(got) != 0 {
			t.Fatalf("listed before the backfill: %v", got)
		}
		for range 2 {
			if !pass(mounted.Load(), context.Background()) {
				t.Fatal("backfill pass did not finish")
			}
		}
		if got := listed(t, pr, "acme"); len(got) != 1 || got["acme-gh-widgets"] != widgetsURL {
			t.Fatalf("after the backfill acme lists %v, want only the repository link", got)
		}
	})
}

// A LINK STANDS WITHOUT THE PROJECTS APP, and the backfill says it has work left.
func TestALinkStandsWhenProjectsIsAway(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	app := mountSync(t)
	linkRepo(t, app, "acme", widgetsURL)
	if pass(mounted.Load(), context.Background()) {
		t.Fatal("the backfill finished with the projects app away")
	}
}
