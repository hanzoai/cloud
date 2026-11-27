package projects

// create_rpc_test.go — the plane's create, through the generated client, over a
// real socket, into the real store.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	projectpeer "github.com/hanzoai/cloud/client/project"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// servedCreate mounts projects as its plugin process does and binds its plane
// socket, so the generated client reaches the op Use registered.
func servedCreate(t *testing.T) *zip.App {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	plane.Bind()
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	app := mountApp(t)
	stop, err := cloud.ServePlane(projectpeer.App, nil)
	if err != nil {
		t.Fatalf("serve plane: %v", err)
	}
	t.Cleanup(func() { _ = stop() })
	return app
}

func stated(org string) context.Context {
	return zip.WithCaller(context.Background(), zip.Caller{Org: org, User: "u_" + org})
}

// A PROJECT MADE OVER THE PLANE IS AN ORDINARY PROJECT, in the caller's org and
// nobody else's: POST /v1/project's own read finds it, public, linked to its
// source.
func TestAProjectMadeOverThePlaneIsTheCallersOwn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := servedCreate(t)
		const src = "https://git.test/hanzoai/build-a-todo-app.git"
		out, err := projectpeer.ProjectsCreate(stated("acme"),
			&client.ProjectIn{Name: "Build a todo app", Slug: "build-a-todo-app", Repo: src})
		if err != nil {
			t.Fatalf("create over the plane: %v", err)
		}
		if out.Slug != "build-a-todo-app" || !strings.HasPrefix(out.ID, "proj") {
			t.Fatalf("answered %+v", out)
		}

		code, body := do(t, app, http.MethodGet, "/v1/project/build-a-todo-app", "acme", nil)
		if code != http.StatusOK {
			t.Fatalf("the caller's own read: %d (%s)", code, body)
		}
		var p projectsProject
		if err := json.Unmarshal(body, &p); err != nil {
			t.Fatalf("json: %v (%s)", err, body)
		}
		if p.Org != "acme" || p.Name != "Build a todo app" || p.Visibility != Public || p.Repo.URL != src {
			t.Fatalf("made %+v, want acme's public project named from the prompt, linked to %s", p, src)
		}
		if code, _ := do(t, app, http.MethodGet, "/v1/project/build-a-todo-app", "other", nil); code != http.StatusNotFound {
			t.Fatalf("another org can read it: %d", code)
		}
	})
}

// A SLUG THE ORG HOLDS IS A 409 AND NEVER RENUMBERED: the caller chose it, and
// the caller picks the next.
func TestATakenSlugIsAConflict(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := servedCreate(t)
		if code, body := do(t, app, http.MethodPost, "/v1/project", "acme", map[string]any{"name": "Todo", "slug": "todo"}); code != http.StatusCreated {
			t.Fatalf("seed: %d (%s)", code, body)
		}
		_, err := projectpeer.ProjectsCreate(stated("acme"), &client.ProjectIn{Name: "Todo", Slug: "todo"})
		if he, ok := errors.AsType[*zip.HTTPError](err); !ok || he.Status != http.StatusConflict {
			t.Fatalf("a taken slug answered %v, want a 409", err)
		}
		// A name is one namespace across every org: another org asking is the 409 too.
		if _, err := projectpeer.ProjectsCreate(stated("other"), &client.ProjectIn{Name: "Todo", Slug: "todo"}); err == nil {
			t.Fatal("another org took a name acme holds")
		} else if he, ok := errors.AsType[*zip.HTTPError](err); !ok || he.Status != http.StatusConflict {
			t.Fatalf("another org's taken name answered %v, want a 409", err)
		}
	})
}

// NO CALLER, NO PROJECT. The org is read off the call, so a call that carries
// none has nowhere to put one.
func TestAProjectNeedsACaller(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		servedCreate(t)
		if _, err := projectpeer.ProjectsCreate(context.Background(), &client.ProjectIn{Name: "Todo", Slug: "todo"}); err == nil {
			t.Fatal("a project was made for nobody")
		}
	})
}

// Slug numbers a name the way POST /v1/project would slug it, and every answer is
// a slug that route would take.
func TestSlug(t *testing.T) {
	long := strings.Repeat("abcdefghij", 5)
	for _, c := range []struct {
		name string
		n    int
		want string
	}{
		{"Build a todo app", 1, "build-a-todo-app"},
		{"Build a todo app", 2, "build-a-todo-app-2"},
		{long, 1, long[:40]},
		{long, 12, long[:37] + "-12"},
		{"chat", 1, ""},
		{"chat", 2, "chat-2"},
		{"这是一个应用", 1, "project"},
		{"这是一个应用", 3, "project-3"},
	} {
		got := Slug(c.name, c.n)
		if got != c.want {
			t.Errorf("Slug(%q, %d) = %q, want %q", c.name, c.n, got, c.want)
		}
		if got != "" && (!slugRE.MatchString(got) || len(got) > 40) {
			t.Errorf("Slug(%q, %d) = %q, which POST /v1/project refuses", c.name, c.n, got)
		}
	}
}
