package agents

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// A coding run's tag names the project it was started on, and the project's
// list of runs (?kind=coding&project=) holds exactly the runs tagged with it. A
// later tag with no project leaves the project as it was.
func TestATaggedProjectListsItsRuns(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		ctx := context.Background()

		open := func() string {
			t.Helper()
			code, b := reqAs(t, app, http.MethodPost, "/v1/agent/sessions", "acme", "alice", false, "", map[string]any{"agent": "coding"})
			if code != http.StatusCreated {
				t.Fatalf("open: %d %s", code, b)
			}
			var v sessionView
			mustJSON(t, b, &v)
			return v.ID
		}
		on, off := open(), open()
		if err := TagSession(ctx, "acme", on, Session{Kind: "coding", Repo: "acme/site", Project: "site"}); err != nil {
			t.Fatal(err)
		}
		if err := TagSession(ctx, "acme", on, Session{Box: "sbx_1"}); err != nil {
			t.Fatal(err)
		}
		if err := TagSession(ctx, "acme", off, Session{Kind: "coding", Repo: "acme/site"}); err != nil {
			t.Fatal(err)
		}

		q := url.Values{"kind": {"coding"}, "project": {"site"}}
		code, b := reqAs(t, app, http.MethodGet, "/v1/agent/sessions?"+q.Encode(), "acme", "alice", false, "", nil)
		if code != http.StatusOK {
			t.Fatalf("list: %d %s", code, b)
		}
		var l sessionList
		mustJSON(t, b, &l)
		if len(l.Sessions) != 1 || l.Sessions[0].ID != on || l.Sessions[0].Project != "site" {
			t.Fatalf("the project lists %+v, want only %s tagged site", l.Sessions, on)
		}

	})
}

// A coding run tagged with a project is a person's run, not the project's
// published story: the deploy turn closes the story the author published, and
// never lands in a later run's session.
func TestADeployClosesThePublishedStoryNotALaterRun(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t, &fakeAI{content: "x"})
		ctx := context.Background()
		story := register(t, app, "acme", map[string]any{"agent": "dev", "project": "shop"})
		if code, b := do(t, app, http.MethodPatch, "/v1/agent/sessions/"+story.ID, "acme", map[string]any{"published": true}); code != http.StatusOK {
			t.Fatalf("publish: %d %s", code, b)
		}
		code, b := reqAs(t, app, http.MethodPost, "/v1/agent/sessions", "acme", "bob", false, "", map[string]any{"agent": "coding"})
		if code != http.StatusCreated {
			t.Fatalf("open: %d %s", code, b)
		}
		var run sessionView
		mustJSON(t, b, &run)
		if err := TagSession(ctx, "acme", run.ID, Session{Kind: "coding", Project: "shop"}); err != nil {
			t.Fatal(err)
		}

		deployWriter{s: mounted}.OnDeploy(ctx, "acme", "shop", "https://shop.hanzo.app", "dep_9")

		code, b = doNoUser(t, app, http.MethodGet, "/v1/agent/builds/acme/shop", "", nil)
		if code != http.StatusOK {
			t.Fatalf("read build: %d %s", code, b)
		}
		var v buildView
		mustJSON(t, b, &v)
		if len(v.Turns) != 1 || !strings.Contains(v.Turns[0].Body, "dep_9") {
			t.Fatalf("the published story did not get the deploy turn: %+v", v.Turns)
		}
		sto, err := mounted.State.storeFor("acme")
		if err != nil {
			t.Fatal(err)
		}
		evs, err := sto.ListEvents(ctx, "acme", run.ID, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			if strings.Contains(e.Payload, "dep_9") {
				t.Fatalf("the deploy turn landed in bob's run: %+v", e)
			}
		}

	})
}
